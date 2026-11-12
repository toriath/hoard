package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/toriath/hoard/internal/hoard"
)

func TestPutTechnicalAttrs(t *testing.T) {
	e := newEnv(t)
	var a struct {
		ID    string         `json:"id"`
		Attrs map[string]any `json:"attrs"`
	}
	name := "Clip"
	created, err := e.svc.CreateAsset(context.Background(), hoard.AssetInput{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	a.ID = created.ID
	e.do("PUT", "/api/v1/assets/"+a.ID+"/technical", `{"attrs":{"duration_s":2.5,"width":640}}`, nil, http.StatusOK, &a)
	if a.Attrs["duration_s"] != 2.5 || a.Attrs["width"] != 640.0 {
		t.Errorf("attrs: %v", a.Attrs)
	}
	e.do("PUT", "/api/v1/assets/"+a.ID+"/technical", `{"attrs":{"license":"x"}}`, nil, http.StatusBadRequest, nil)
	e.do("PUT", "/api/v1/assets/"+a.ID+"/technical", `{"attrs":{"channels":9999}}`, nil, http.StatusBadRequest, nil)
	e.do("PUT", "/api/v1/assets/ast_aaaaaaaaaaaa/technical", `{"attrs":{"width":1}}`, nil, http.StatusNotFound, nil)
	// A cross-site request must not ride on the session cookie.
	e.do("PUT", "/api/v1/assets/"+a.ID+"/technical", `{"attrs":{"width":2}}`, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden, nil)
	e.send(&http.Client{}, "PUT", "/api/v1/assets/"+a.ID+"/technical", `{"attrs":{"width":1}}`, nil, http.StatusUnauthorized, nil)
}
