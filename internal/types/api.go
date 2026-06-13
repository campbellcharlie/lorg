package types

type AddRequestBodyType struct {
	Url         string  `json:"url"`
	Index       float64 `json:"index"`
	Request     string  `json:"request"`
	Response    string  `json:"response"`
	GeneratedBy string  `json:"generated_by"`
	Note        string  `json:"note,omitempty"`
	// Project tags the saved _data row so it is returned by project-scoped
	// reads (query project:<id>). Empty leaves the row untagged, matching the
	// proxy's _data tag at apps/app/proxy_rawproxy.go (project column).
	Project string `json:"project,omitempty"`
}
