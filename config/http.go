package config

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	ctyjson "github.com/zclconf/go-cty/cty/json"
	"go.uber.org/zap"
)

type HttpServer struct {
	BaseServer
	Logger    *zap.Logger
	Server    *http.Server
	TLSConfig *tls.Config
}

type HttpServerDefinition struct {
	Listen      string                  `hcl:"listen"`
	TLS         *TLSConfig              `hcl:"tls,block"`
	DefRange    hcl.Range               `hcl:",def_range"`
	StaticFiles []staticFilesDefinition `hcl:"files,block"`
	Handlers    []handlerDefinition     `hcl:"handle,block"`
}

type staticFilesDefinition struct {
	UrlPath   string    `hcl:"urlpath,label"`
	Directory string    `hcl:"directory"`
	Disabled  bool      `hcl:"disabled,optional"`
	DefRange  hcl.Range `hcl:",def_range"`
}

type handlerDefinition struct {
	Route    string         `hcl:"route,label"`
	Action   hcl.Expression `hcl:"action"`
	Handler  hcl.Expression `hcl:"handler"`
	Disabled bool           `hcl:"disabled,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type HandlerServer interface {
	Listener
	GetHandler() http.Handler
}

func init() {
	RegisterServerType("http", ProcessHttpServerBlock)
}

func ProcessHttpServerBlock(config *Config, block *hcl.Block, remainingBody hcl.Body) (Listener, hcl.Diagnostics) {
	serverDef := HttpServerDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.evalCtx, &serverDef)
	if diags.HasErrors() {
		return nil, diags
	}

	httpServers, ok := config.Servers["http"]
	if !ok {
		httpServers = make(map[string]Listener)
		config.Servers["http"] = httpServers
	}

	existing, ok := httpServers[block.Labels[1]]
	if ok {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Http server already defined",
				Detail:   fmt.Sprintf("Http server %s already defined at %s", block.Labels[1], existing.GetDefRange()),
				Subject:  &serverDef.DefRange,
			},
		}
	}

	mux := http.NewServeMux()

	for _, file := range serverDef.StaticFiles {
		if file.Disabled {
			continue
		}

		if strings.Contains(file.UrlPath, " ") {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid URL path",
					Detail:   fmt.Sprintf("Invalid urlpath: %s", file.UrlPath),
					Subject:  &file.DefRange,
				},
			}
		}

		path := strings.TrimSuffix(file.UrlPath, "/") + "/"

		mux.Handle(path, NewLoggingMiddleware(config.Logger,
			http.StripPrefix(path, http.FileServer(http.Dir(file.Directory)))))
	}

	for _, handlerDef := range serverDef.Handlers {
		if handlerDef.Disabled {
			continue
		}

		if IsExpressionProvided(handlerDef.Handler) && IsExpressionProvided(handlerDef.Action) || !IsExpressionProvided(handlerDef.Handler) && !IsExpressionProvided(handlerDef.Action) {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Exactly one of handler or action must be specified",
					Subject:  &handlerDef.DefRange,
				},
			}
		}

		if IsExpressionProvided(handlerDef.Action) {
			mux.Handle(handlerDef.Route, NewLoggingMiddleware(config.Logger, &httpAction{config: config, actionExpr: handlerDef.Action}))
		} else {
			handler, diags := GetServerFromExpression(config, handlerDef.Handler)
			if diags.HasErrors() {
				return nil, diags
			}

			handlerServer, ok := handler.(HandlerServer)
			if !ok {
				return nil, hcl.Diagnostics{
					&hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Provided handler does not implement Handler interface",
						Subject:  &handlerDef.DefRange,
					},
				}
			}

			mux.Handle(handlerDef.Route, NewLoggingMiddleware(config.Logger, handlerServer.GetHandler()))
		}
	}

	server := &HttpServer{
		Logger: config.Logger,
		BaseServer: BaseServer{
			Name:     block.Labels[1],
			DefRange: serverDef.DefRange,
		},
		Server: &http.Server{
			Addr:    serverDef.Listen,
			Handler: mux,
		},
	}

	if serverDef.TLS != nil {
		tlsCfg, err := serverDef.TLS.BuildTLSServerConfig(config.BaseDir)
		if err != nil {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid TLS configuration",
					Detail:   err.Error(),
					Subject:  &serverDef.TLS.DefRange,
				},
			}
		}
		server.TLSConfig = tlsCfg
	}

	config.Startables = append(config.Startables, server)

	return server, nil
}

func (h *HttpServer) Start() error {
	go func() {
		h.Logger.Info("Starting HTTP server", zap.String("name", h.Name), zap.String("addr", h.Server.Addr))
		var err error
		if h.TLSConfig != nil {
			h.Server.TLSConfig = h.TLSConfig
			err = h.Server.ListenAndServeTLS("", "")
		} else {
			err = h.Server.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			h.Logger.Error("Failed to start HTTP server", zap.Error(err))
		}
	}()

	return nil
}

type loggingMiddleware struct {
	next   http.Handler
	logger *zap.Logger
}

func NewLoggingMiddleware(logger *zap.Logger, next http.Handler) http.Handler {
	return &loggingMiddleware{
		logger: logger,
		next:   next,
	}
}

func (l *loggingMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// TODO: Log status code
	l.logger.Info("Request", zap.String("method", r.Method), zap.String("url", r.URL.String()))
	l.next.ServeHTTP(w, r)
}

type httpAction struct {
	config     *Config
	actionExpr hcl.Expression
}

func (h *httpAction) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	evalCtx, err := getHttpActionEvalContext(h.config, w, r)
	if err != nil {
		h.config.Logger.Error("Error building evaluation context", zap.Error(err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	_, diags := h.actionExpr.Value(evalCtx)
	if diags.HasErrors() {
		h.config.Logger.Error("Error executing action", zap.Error(diags))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func getHttpActionEvalContext(config *Config, w http.ResponseWriter, r *http.Request) (*hcl.EvalContext, error) {
	builder := hclutil.NewEvalContext(r.Context()).
		WithAttribute("request", types.BuildHTTPRequestObject(r)).
		WithFunctions(getHttpContextFunctions(w, r))

	return builder.BuildEvalContext(config.evalCtx)
}

func getHttpContextFunctions(w http.ResponseWriter, r *http.Request) map[string]function.Function {
	return map[string]function.Function{
		"redirect": makeRedirectFunc(w, r),
		"respond":  makeRespondFunc(w, r),
		"setheader": makeSetHeaderFunc(w, r),
	}
}

func makeRedirectFunc(w http.ResponseWriter, r *http.Request) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "code", Type: cty.Number},
			{Name: "url", Type: cty.String},
		},
		Type: function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			code, _ := args[0].AsBigFloat().Int64()
			// No-op: just return true without logging
			http.Redirect(w, r, args[1].AsString(), int(code))
			return cty.True, nil
		},
	})
}

func makeRespondFunc(w http.ResponseWriter, _ *http.Request) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "code", Type: cty.Number},
			{Name: "body", Type: cty.DynamicPseudoType},
		},
		Type: function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			code, _ := args[0].AsBigFloat().Int64()
			// No-op: just return true without logging
			w.WriteHeader(int(code))
			if args[1].Type() == cty.String {
				w.Write([]byte(args[1].AsString()))
			} else {
				w.Header().Set("Content-Type", "application/json")
				body, err := ctyjson.Marshal(args[1], args[1].Type())
				if err != nil {
					return cty.False, fmt.Errorf("failed to marshal body: %w", err)
				}
				w.Write(body)
			}
			return cty.True, nil
		},
	})
}

func makeSetHeaderFunc(w http.ResponseWriter, _ *http.Request) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "key", Type: cty.String},
			{Name: "value", Type: cty.String},
		},
		Type: function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			// No-op: just return true without logging
			w.Header().Set(args[0].AsString(), args[1].AsString())

			return cty.True, nil
		},
	})
}
