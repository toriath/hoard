package server

// routesDelivery registers the REST endpoints for getting assets into game
// repositories. api requires a signed-in user or an API token; pub is only
// for signed, expiring links (verify the signature in the handler).
func (s *Server) routesDelivery(api, pub route) {
	_, _ = api, pub
}
