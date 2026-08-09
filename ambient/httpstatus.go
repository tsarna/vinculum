package ambient

import (
	"net/http"
	"strconv"

	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterAmbientProvider("http_status", func(_ *cfg.Config) cty.Value {
		return getStatusCodeObject()
	}, cfg.WithNamespaceSchema(httpStatusNamespace))
}

// httpStatusNamespace describes `http_status`. Every member says the same thing
// — it is a status code — and differs only in which one, so the summary is
// uniform and the code itself is the emitted value.
var httpStatusNamespace = cfg.NamespaceSchema{
	Summary: "Constants for the HTTP status codes.",
	Doc: "Names are PascalCase, matching Go's `net/http` package: `http_status.OK` is `200`, " +
		"`http_status.NotFound` is `404`. All standard 1xx–5xx codes are included. Prefer these " +
		"to bare integers — `status = http_status.NoContent` says what `status = 204` means.",
	DocPage:              "config.md#variables",
	Constant:             true,
	UniformMemberSummary: "The numeric HTTP status code.",
	Members: map[string]cfg.MemberMeta{
		"bycode": {
			Summary: "Status code to name, keyed by the code as a string.",
			Doc: "For turning a code you were given back into something readable: " +
				"`http_status.bycode[\"404\"]` is `\"NotFound\"`. HCL coerces an integer key to a " +
				"string, so `http_status.bycode[404]` works too.",
		},
	},
}

func getStatusCodeObject() cty.Value {
	codes := make(map[string]cty.Value)
	for name, code := range statusCodes {
		codes[name] = cty.NumberIntVal(int64(code))
	}
	codes["bycode"] = getStatusCodeMap()
	return cty.ObjectVal(codes)
}

func getStatusCodeMap() cty.Value {
	codes := make(map[string]cty.Value)
	for name, code := range statusCodes {
		codes[strconv.Itoa(code)] = cty.StringVal(name)
	}
	return cty.MapVal(codes)
}

var statusCodes = map[string]int{
	"Continue":           http.StatusContinue,
	"SwitchingProtocols": http.StatusSwitchingProtocols,
	"Processing":         http.StatusProcessing,
	"EarlyHints":         http.StatusEarlyHints,

	"OK":                   http.StatusOK,
	"Created":              http.StatusCreated,
	"Accepted":             http.StatusAccepted,
	"NonAuthoritativeInfo": http.StatusNonAuthoritativeInfo,
	"NoContent":            http.StatusNoContent,
	"ResetContent":         http.StatusResetContent,
	"PartialContent":       http.StatusPartialContent,
	"MultiStatus":          http.StatusMultiStatus,
	"AlreadyReported":      http.StatusAlreadyReported,
	"IMUsed":               http.StatusIMUsed,

	"MultipleChoices":  http.StatusMultipleChoices,
	"MovedPermanently": http.StatusMovedPermanently,
	"Found":            http.StatusFound,
	"SeeOther":         http.StatusSeeOther,
	"NotModified":      http.StatusNotModified,
	"UseProxy":         http.StatusUseProxy,

	"TemporaryRedirect": http.StatusTemporaryRedirect,
	"PermanentRedirect": http.StatusPermanentRedirect,

	"BadRequest":                   http.StatusBadRequest,
	"Unauthorized":                 http.StatusUnauthorized,
	"PaymentRequired":              http.StatusPaymentRequired,
	"Forbidden":                    http.StatusForbidden,
	"NotFound":                     http.StatusNotFound,
	"MethodNotAllowed":             http.StatusMethodNotAllowed,
	"NotAcceptable":                http.StatusNotAcceptable,
	"ProxyAuthRequired":            http.StatusProxyAuthRequired,
	"RequestTimeout":               http.StatusRequestTimeout,
	"Conflict":                     http.StatusConflict,
	"Gone":                         http.StatusGone,
	"LengthRequired":               http.StatusLengthRequired,
	"PreconditionFailed":           http.StatusPreconditionFailed,
	"RequestEntityTooLarge":        http.StatusRequestEntityTooLarge,
	"RequestURITooLong":            http.StatusRequestURITooLong,
	"UnsupportedMediaType":         http.StatusUnsupportedMediaType,
	"RequestedRangeNotSatisfiable": http.StatusRequestedRangeNotSatisfiable,
	"ExpectationFailed":            http.StatusExpectationFailed,
	"Teapot":                       http.StatusTeapot,
	"MisdirectedRequest":           http.StatusMisdirectedRequest,
	"UnprocessableEntity":          http.StatusUnprocessableEntity,
	"Locked":                       http.StatusLocked,
	"FailedDependency":             http.StatusFailedDependency,
	"TooEarly":                     http.StatusTooEarly,
	"UpgradeRequired":              http.StatusUpgradeRequired,
	"PreconditionRequired":         http.StatusPreconditionRequired,
	"TooManyRequests":              http.StatusTooManyRequests,
	"RequestHeaderFieldsTooLarge":  http.StatusRequestHeaderFieldsTooLarge,
	"UnavailableForLegalReasons":   http.StatusUnavailableForLegalReasons,

	"InternalServerError":           http.StatusInternalServerError,
	"NotImplemented":                http.StatusNotImplemented,
	"BadGateway":                    http.StatusBadGateway,
	"ServiceUnavailable":            http.StatusServiceUnavailable,
	"GatewayTimeout":                http.StatusGatewayTimeout,
	"HTTPVersionNotSupported":       http.StatusHTTPVersionNotSupported,
	"VariantAlsoNegotiates":         http.StatusVariantAlsoNegotiates,
	"InsufficientStorage":           http.StatusInsufficientStorage,
	"LoopDetected":                  http.StatusLoopDetected,
	"NotExtended":                   http.StatusNotExtended,
	"NetworkAuthenticationRequired": http.StatusNetworkAuthenticationRequired,
}
