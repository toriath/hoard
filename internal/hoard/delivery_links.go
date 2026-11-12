package hoard

import (
	"fmt"
	"net/url"
)

// AtlasURL is a signed link to download a packed atlas as a zip. The signature
// covers the asset ids and every option.
func (s *Service) AtlasURL(base string, ids []string, o AtlasOptions) string {
	exp, sig := s.Sign(AtlasPayload(ids, o), repoLinkTTL)
	q := AtlasQuery(ids, o)
	q.Set("exp", exp)
	q.Set("sig", sig)
	return base + "/api/v1/atlas?" + q.Encode()
}

// DeriveURL is a signed link that renders (and caches) a derivative of one
// stored file. The signature covers the asset, the file and every option.
func (s *Service) DeriveURL(base, assetID, filename string, o DeriveOptions) string {
	exp, sig := s.Sign("derive:"+assetID+":"+filename+":"+o.Payload(), repoLinkTTL)
	q := o.Query()
	q.Set("exp", exp)
	q.Set("sig", sig)
	return fmt.Sprintf("%s/api/v1/derive/%s/%s?%s", base, assetID, url.PathEscape(filename), q.Encode())
}
