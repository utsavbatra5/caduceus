package main

import (
	"fmt"
	"github.com/xmidt-org/bascule"
	"github.com/xmidt-org/candlelight"
	"github.com/xmidt-org/webpa-common/logging"
	"net/http"

	"context"
	"github.com/SermoDigital/jose/jwt"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/metrics/provider"
	"github.com/gorilla/mux"
	"github.com/justinas/alice"
	"github.com/spf13/viper"
	"github.com/xmidt-org/ancla"
	"github.com/xmidt-org/webpa-common/secure"
	"github.com/xmidt-org/webpa-common/secure/handler"
	"github.com/xmidt-org/webpa-common/secure/key"
)

const (
	baseURI = "api"
	version = "v3"
)

type JWTValidator struct {
	// JWTKeys is used to create the key.Resolver for JWT verification keys
	Keys key.ResolverFactory

	// Custom is an optional configuration section that defines
	// custom rules for validation over and above the standard RFC rules.
	Custom secure.JWTValidatorFactory
}

func SetLogger(logger log.Logger) func(delegate http.Handler) http.Handler {
	return func(delegate http.Handler) http.Handler {
		return http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				kvs := []interface{}{"requestHeaders", r.Header, "requestURL", r.URL.EscapedPath(), "method", r.Method}
				kvs, _ = candlelight.AppendTraceInfo(r.Context(), kvs)
				ctx := r.WithContext(logging.WithLogger(r.Context(), log.With(logger, kvs...)))
				delegate.ServeHTTP(w, ctx)
			})
	}
}

func GetLogger(ctx context.Context) bascule.Logger {
	logger := log.With(logging.GetLogger(ctx), "ts", log.DefaultTimestampUTC, "caller", log.DefaultCaller)
	return logger
}

func NewPrimaryHandler(l log.Logger, v *viper.Viper, sw *ServerHandler, webhookSvc ancla.Service, metricsRegistry provider.Provider, router *mux.Router) (*mux.Router, error) {

	validator, err := getValidator(v)
	if err != nil {
		return nil, err
	}

	authHandler := handler.AuthorizationHandler{
		HeaderName:          "Authorization",
		ForbiddenStatusCode: 403,
		Validator:           validator,
		Logger:              l,
	}

	authorizationDecorator := alice.New(SetLogger(l), authHandler.Decorate)

	return configServerRouter(router, authorizationDecorator, sw, webhookSvc, metricsRegistry), nil
}

func configServerRouter(router *mux.Router, primaryHandler alice.Chain, serverWrapper *ServerHandler, webhookSvc ancla.Service, metricsRegistry provider.Provider) *mux.Router {
	var singleContentType = func(r *http.Request, _ *mux.RouteMatch) bool {
		return len(r.Header["Content-Type"]) == 1 // require single specification for Content-Type Header
	}

	router.Handle("/"+fmt.Sprintf("%s/%s", baseURI, version)+"/notify", primaryHandler.Then(serverWrapper)).Methods("POST").HeadersRegexp("Content-Type", "application/msgpack").MatcherFunc(singleContentType)

	addWebhookHandler := ancla.NewAddWebhookHandler(webhookSvc, ancla.HandlerConfig{
		MetricsProvider: metricsRegistry,
	})
	// register webhook end points
	router.Handle("/hook", primaryHandler.Then(addWebhookHandler)).Methods("POST")

	return router
}

func getValidator(v *viper.Viper) (validator secure.Validator, err error) {
	var jwtVals []JWTValidator

	v.UnmarshalKey("jwtValidators", &jwtVals)

	// if a JWTKeys section was supplied, configure a JWS validator
	// and append it to the chain of validators
	validators := make(secure.Validators, 0, len(jwtVals))

	for _, validatorDescriptor := range jwtVals {
		var keyResolver key.Resolver
		keyResolver, err = validatorDescriptor.Keys.NewResolver()
		if err != nil {
			validator = validators
			return
		}

		validators = append(
			validators,
			secure.JWSValidator{
				DefaultKeyId:  DEFAULT_KEY_ID,
				Resolver:      keyResolver,
				JWTValidators: []*jwt.Validator{validatorDescriptor.Custom.New()},
			},
		)
	}

	// TODO: This should really be part of the unmarshalled validators somehow
	basicAuth := v.GetStringSlice("authHeader")
	for _, authValue := range basicAuth {
		validators = append(
			validators,
			secure.ExactMatchValidator(authValue),
		)
	}

	validator = validators

	return
}
