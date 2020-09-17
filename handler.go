package handler

import (
	"encoding/json"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/graphql-go/graphql"

	"context"

	"github.com/graphql-go/graphql/gqlerrors"
)

var (
	MaxUploadMemorySize = int64(1024 * 1024 * 10)
	Title               = "GraphQL Playground"
)

const (
	ContentTypeJSON              = "application/json"
	ContentTypeGraphQL           = "application/graphql"
	ContentTypeFormURLEncoded    = "application/x-www-form-urlencoded"
	ContentTypeMultipartFormData = "multipart/form-data"
)

type ResultCallbackFn func(ctx context.Context, params *graphql.Params, result *graphql.Result, responseBody []byte)

type Handler struct {
	Schema           *graphql.Schema
	pretty           bool
	graphiql         bool
	rootObjectFn     RootObjectFn
	resultCallbackFn ResultCallbackFn
	formatErrorFn    func(err error) gqlerrors.FormattedError
}

type RequestOptions struct {
	Query         string                             `json:"query" url:"query" schema:"query"`
	Variables     map[string]interface{}             `json:"variables" url:"variables" schema:"variables"`
	OperationName string                             `json:"operationName" url:"operationName" schema:"operationName"`
	File          map[string][]*multipart.FileHeader `json:"-"`
}

func getFromMultipartForm(form *multipart.Form) *RequestOptions {
	values := url.Values(form.Value)
	query := values.Get("query")
	if query != "" {
		// get variables map
		variables := make(map[string]interface{}, len(values))
		variablesStr := values.Get("variables")
		_ = json.Unmarshal([]byte(variablesStr), &variables)
		return &RequestOptions{
			Query:         query,
			Variables:     variables,
			OperationName: values.Get("operationName"),
			File:          form.File,
		}
	}
	return nil
}

func getFromForm(values url.Values) *RequestOptions {
	query := values.Get("query")
	if query != "" {
		// get variables map
		variables := make(map[string]interface{}, len(values))
		variablesStr := values.Get("variables")
		_ = json.Unmarshal([]byte(variablesStr), &variables)
		return &RequestOptions{
			Query:         query,
			Variables:     variables,
			OperationName: values.Get("operationName"),
		}
	}
	return nil
}

// RequestOptions Parses a http.Request into GraphQL request options struct
func NewRequestOptions(r *http.Request) *RequestOptions {
	if reqOpt := getFromForm(r.URL.Query()); reqOpt != nil {
		return reqOpt
	}

	if r.Method != http.MethodPost {
		return &RequestOptions{}
	}

	if r.Body == nil {
		return &RequestOptions{}
	}

	contentTypeStr := r.Header.Get("Content-Type")
	contentTypeTokens := strings.Split(contentTypeStr, ";")
	contentType := contentTypeTokens[0]

	switch contentType {
	case ContentTypeGraphQL:
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			return &RequestOptions{}
		}
		return &RequestOptions{
			Query: string(body),
		}
	case ContentTypeFormURLEncoded:
		if err := r.ParseForm(); err != nil {
			return &RequestOptions{}
		}
		if reqOpt := getFromForm(r.PostForm); reqOpt != nil {
			return reqOpt
		}
		return &RequestOptions{}
	case ContentTypeMultipartFormData:
		if err := r.ParseMultipartForm(MaxUploadMemorySize); err != nil {
			return &RequestOptions{}
		}
		if reqOpt := getFromMultipartForm(r.MultipartForm); reqOpt != nil {
			return reqOpt
		}
		return &RequestOptions{}
	case ContentTypeJSON:
		fallthrough
	default:
		var opts RequestOptions
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			return &opts
		}
		_ = json.Unmarshal(body, &opts)
		return &opts
	}
}

// ContextHandler provides an entrypoint into executing graphQL queries with a
// user-provided context.
func (h *Handler) ContextHandler(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	// get query
	opts := NewRequestOptions(r)
	// execute graphql query
	params := graphql.Params{
		Schema:         *h.Schema,
		RequestString:  opts.Query,
		VariableValues: opts.Variables,
		OperationName:  opts.OperationName,
		Context:        ctx,
	}
	if h.rootObjectFn != nil {
		params.RootObject = h.rootObjectFn(ctx, r, opts)
	}
	result := graphql.Do(params)
	if formatErrorFn := h.formatErrorFn; formatErrorFn != nil && len(result.Errors) > 0 {
		formatted := make([]gqlerrors.FormattedError, len(result.Errors))
		for i, formattedError := range result.Errors {
			formatted[i] = formatErrorFn(formattedError.OriginalError())
		}
		result.Errors = formatted
	}
	if h.graphiql {
		acceptHeader := r.Header.Get("Accept")
		_, raw := r.URL.Query()["raw"]
		if !raw && !strings.Contains(acceptHeader, "application/json") && strings.Contains(acceptHeader, "text/html") {
			renderGraphiQL(w, params)
			return
		}
	}
	// use proper JSON Header
	w.Header().Add("Content-Type", "application/json; charset=utf-8")
	var buff []byte
	if h.pretty {
		w.WriteHeader(http.StatusOK)
		buff, _ = json.MarshalIndent(result, "", "\t")

		_, _ = w.Write(buff)
	} else {
		w.WriteHeader(http.StatusOK)
		buff, _ = json.Marshal(result)

		_, _ = w.Write(buff)
	}
	if h.resultCallbackFn != nil {
		h.resultCallbackFn(ctx, &params, result, buff)
	}
}

// ServeHTTP provides an entrypoint into executing graphQL queries.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.ContextHandler(r.Context(), w, r)
}

// RootObjectFn allows a user to generate a RootObject per request
type RootObjectFn func(ctx context.Context, r *http.Request, opts *RequestOptions) map[string]interface{}

type Config struct {
	Title            string
	Schema           *graphql.Schema
	Pretty           bool
	GraphiQL         bool
	RootObjectFn     RootObjectFn
	ResultCallbackFn ResultCallbackFn
	FormatErrorFn    func(err error) gqlerrors.FormattedError
}

func NewConfig() *Config {
	return &Config{
		Schema:   nil,
		Pretty:   true,
		GraphiQL: true,
	}
}

func New(p *Config) *Handler {
	if p == nil {
		p = NewConfig()
	}

	if p.Schema == nil {
		panic("undefined GraphQL schema")
	}
	if p.Title != "" {
		Title = p.Title
	}
	return &Handler{
		Schema:           p.Schema,
		pretty:           p.Pretty,
		graphiql:         p.GraphiQL,
		rootObjectFn:     p.RootObjectFn,
		resultCallbackFn: p.ResultCallbackFn,
		formatErrorFn:    p.FormatErrorFn,
	}
}
