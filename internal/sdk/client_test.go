// file: client_test.go

package sdk

import (
	"testing"

	"github.com/campbellcharlie/lorg/internal/types"
	"github.com/campbellcharlie/lorg/internal/utils"
	"github.com/stretchr/testify/assert"
)

const (
	defaultURL = "http://127.0.0.1:8090"
)

// REMEMBER to start lorg before running these tests

func TestAuthorizeAnonymous(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{
			name:    "Empty credentials",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(defaultURL)
			err := c.Authorize()
			assert.Equal(t, tt.wantErr, err != nil)
		})
	}
}

func TestClient_SitemapNew(t *testing.T) {
	defaultClient := NewClient(defaultURL)
	tests := []struct {
		name    string
		client  *Client
		body    types.SitemapGet
		wantErr bool
		wantID  bool
	}{
		{
			name:   "Create with body",
			client: defaultClient,
			body: types.SitemapGet{
				Host:     "https://2example2.com",
				Path:     "/folder/subfolder/test",
				Type:     "file",
				Query:    "?test=1&test2=2",
				Fragment: "#frag",
				Data:     utils.RandomString(15),
			},
			wantErr: false,
			wantID:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.client.SitemapNew(tt.body)
			assert.Equal(t, tt.wantErr, err != nil, err)
		})
	}
}
