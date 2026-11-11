package server

// routesDelivery registers the REST endpoints of one feature area. api requires a
// signed-in user or an API token; pub is only for signed, expiring links
// (verify the signature in the handler).
func (s *Server) routesDelivery(api, pub route) {
	_, _ = api, pub
}
