// file: client_test.go

package sdk

import (
	"testing"
	"time"

	"github.com/campbellcharlie/lorg/internal/types"
	"github.com/campbellcharlie/lorg/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	defaultURL = "http://127.0.0.1:8090"

	// Test collection names (previously from migrations package).
	testPostsPublic = "posts_public"
	testPostsAdmin  = "posts_admin"
	testPostsUser   = "posts_user"
	testAdminEmail  = "new@example.com"
	testAdminPass   = "1234567890"
	testUserEmail   = "user@example.com"
	testUserPass    = "1234567890"
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

func TestListAccess(t *testing.T) {
	type auth struct {
		email    string
		password string
	}
	tests := []struct {
		name       string
		admin      auth
		user       auth
		collection string
		wantResult bool
		wantErr    bool
	}{
		{
			name:       "With admin credentials - posts_admin",
			admin:      auth{email: testAdminEmail, password: testAdminPass},
			collection: testPostsAdmin,
			wantResult: true,
			wantErr:    false,
		},
		{
			name:       "Without credentials - posts_admin",
			collection: testPostsAdmin,
			wantErr:    true,
		},
		{
			name:       "Without credentials - posts_public",
			collection: testPostsPublic,
			wantResult: true,
			wantErr:    false,
		},
		{
			name:       "Without credentials - posts_user",
			collection: testPostsUser,
			wantResult: false,
			wantErr:    false,
		},
		{
			name:       "With user credentials - posts_user",
			user:       auth{email: testUserEmail, password: testUserPass},
			collection: testPostsUser,
			wantResult: true,
			wantErr:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(defaultURL)
			if tt.admin.email != "" {
				c = NewClient(defaultURL, WithAdminEmailPassword(tt.admin.email, tt.admin.password))
			} else if tt.user.email != "" {
				c = NewClient(defaultURL, WithUserEmailPassword(tt.user.email, tt.user.password))
			}
			r, err := c.List(tt.collection, types.ParamsList{})
			assert.Equal(t, tt.wantErr, err != nil, err)
			assert.Equal(t, tt.wantResult, r.TotalItems > 0)
		})
	}
}

func TestAuthorizeEmailPassword(t *testing.T) {
	type args struct {
		email    string
		password string
	}
	tests := []struct {
		name    string
		admin   args
		user    args
		wantErr bool
	}{
		{
			name:    "Valid credentials admin",
			admin:   args{email: testAdminEmail, password: testAdminPass},
			wantErr: false,
		},
		{
			name:    "Invalid credentials admin",
			admin:   args{email: "invalid_" + testAdminEmail, password: "no_admin@admin.com"},
			wantErr: true,
		},
		{
			name:    "Valid credentials user",
			user:    args{email: testUserEmail, password: testUserPass},
			wantErr: false,
		},
		{
			name:    "Invalid credentials user",
			user:    args{email: "invalid_" + testUserEmail, password: testUserPass},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(defaultURL)
			if tt.admin.email != "" {
				c = NewClient(defaultURL, WithAdminEmailPassword(tt.admin.email, tt.admin.password))
			} else if tt.user.email != "" {
				c = NewClient(defaultURL, WithUserEmailPassword(tt.user.email, tt.user.password))
			}
			err := c.Authorize()
			assert.Equal(t, tt.wantErr, err != nil)
		})
	}
}

func TestClient_List(t *testing.T) {
	defaultClient := NewClient(defaultURL)

	tests := []struct {
		name       string
		client     *Client
		collection string
		params     types.ParamsList
		wantResult bool
		wantErr    bool
	}{
		{
			name:       "List with no params",
			client:     defaultClient,
			collection: testPostsPublic,
			wantErr:    false,
			wantResult: true,
		},
		{
			name:       "List no results - query",
			client:     defaultClient,
			collection: testPostsPublic,
			params: types.ParamsList{
				Filters: "field='some_random_value'",
			},
			wantErr:    false,
			wantResult: false,
		},
		{
			name:       "List no results - invalid query",
			client:     defaultClient,
			collection: testPostsPublic,
			params: types.ParamsList{
				Filters: "field~~~some_random_value'",
			},
			wantErr:    true,
			wantResult: false,
		},
		{
			name:       "List invalid collection",
			client:     defaultClient,
			collection: "invalid_collection",
			wantErr:    true,
			wantResult: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.client.List(tt.collection, tt.params)
			assert.Equal(t, tt.wantErr, err != nil, err)
			assert.Equal(t, tt.wantResult, got.TotalItems > 0)
		})
	}
}

func TestClient_Delete(t *testing.T) {
	client := NewClient(defaultURL)
	field := "value_" + time.Now().Format(time.StampMilli)

	err := client.Delete(testPostsPublic, "non_existing_id")
	assert.Error(t, err)

	resultCreated, err := client.Create(testPostsPublic, map[string]any{
		"field": field,
	})
	assert.NoError(t, err)
	assert.NotEmpty(t, resultCreated.ID)

	resultList, err := client.List(testPostsPublic, types.ParamsList{Filters: "id='" + resultCreated.ID + "'"})
	assert.NoError(t, err)
	assert.Len(t, resultList.Items, 1)

	err = client.Delete(testPostsPublic, resultCreated.ID)
	assert.NoError(t, err)

	resultList, err = client.List(testPostsPublic, types.ParamsList{Filters: "id='" + resultCreated.ID + "'"})
	assert.NoError(t, err)
	assert.Len(t, resultList.Items, 0)
}

func TestClient_Update(t *testing.T) {
	client := NewClient(defaultURL)
	field := "value_" + time.Now().Format(time.StampMilli)

	err := client.Update(testPostsPublic, "non_existing_id", map[string]any{
		"field": field,
	})
	assert.Error(t, err)

	resultCreated, err := client.Create(testPostsPublic, map[string]any{
		"field": field,
	})
	assert.NoError(t, err)
	assert.NotEmpty(t, resultCreated.ID)

	resultList, err := client.List(testPostsPublic, types.ParamsList{Filters: "id='" + resultCreated.ID + "'"})
	assert.NoError(t, err)
	require.Len(t, resultList.Items, 1)
	assert.Equal(t, field, resultList.Items[0]["field"])

	err = client.Update(testPostsPublic, resultCreated.ID, map[string]any{
		"field": field + "_updated",
	})
	assert.NoError(t, err)

	resultList, err = client.List(testPostsPublic, types.ParamsList{Filters: "id='" + resultCreated.ID + "'"})
	assert.NoError(t, err)
	require.Len(t, resultList.Items, 1)
	assert.Equal(t, field+"_updated", resultList.Items[0]["field"])
}

func TestClient_Create(t *testing.T) {
	defaultClient := NewClient(defaultURL)
	defaultBody := map[string]interface{}{
		"field": "value_" + time.Now().Format(time.StampMilli),
	}

	tests := []struct {
		name       string
		client     *Client
		collection string
		body       any
		wantErr    bool
		wantID     bool
	}{
		{
			name:       "Create with no body",
			client:     defaultClient,
			collection: testPostsPublic,
			wantErr:    true,
		},
		{
			name:       "Create with body",
			client:     defaultClient,
			collection: testPostsPublic,
			body:       defaultBody,
			wantErr:    false,
			wantID:     true,
		},
		{
			name:       "Create invalid collections",
			client:     defaultClient,
			collection: "invalid_collection",
			body:       defaultBody,
			wantErr:    true,
		},
		{
			name:       "Create no auth",
			client:     defaultClient,
			collection: testPostsUser,
			body:       defaultBody,
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := tt.client.Create(tt.collection, tt.body)
			assert.Equal(t, tt.wantErr, err != nil, err)
			assert.Equal(t, tt.wantID, r.ID != "")
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
