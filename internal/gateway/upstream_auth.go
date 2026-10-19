package gateway

import (
	"net/http"

	"api-gateway/internal/config"
)

// clearClientCredentials removes the two authentication headers used by the
// gateway. It is only called after a gateway credential has been accepted.
func clearClientCredentials(h http.Header) {
	h.Del("Authorization")
	h.Del("x-api-key")
}

// prepareConvertedChatAuth keeps gateway credentials out of protocol
// conversion requests. The selected project's own credential is injected for
// the Chat Completions upstream, regardless of which client protocol arrived.
func prepareConvertedChatAuth(h http.Header, gatewayAuth string, client http.Header, project config.ResolvedProject) {
	if gatewayAuth != "" && normalizeGatewayAuth(client.Get("Authorization"), client.Get("x-api-key"), gatewayAuth) {
		clearClientCredentials(h)
	}
	if project.APIKey != "" {
		clearClientCredentials(h)
		h.Set("Authorization", "Bearer "+project.APIKey)
	}
}
