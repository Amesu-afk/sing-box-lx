package libbox

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPClientAllowsLargeGenericResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", (1<<20)+1)))
	}))
	defer server.Close()

	client := NewHTTPClient()
	defer client.Close()
	request := client.NewRequest()
	if err := request.SetURL(server.URL); err != nil {
		t.Fatalf("SetURL: %v", err)
	}
	response, err := request.Execute()
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err = response.GetContent(); err != nil {
		t.Fatalf("generic client must not inherit Android subscription limits: %v", err)
	}
}
