package ambient

import (
	"net/http"
	"strconv"

	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	cfg.RegisterAmbientProvider("httpstatus", func(_ *cfg.Config) cty.Value {
		return getStatusCodeObject()
	})
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
