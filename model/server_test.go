package model

import (
	"strings"
	"testing"

	"github.com/railzen/nezha-zero/pkg/utils"
)

func TestServerMarshal(t *testing.T) {
	patterns := []string{
		"asd > asd",
		"asd \" asd",
		"asd } asd",
	}

	for i := 0; i < len(patterns); i++ {
		server := Server{
			Name: patterns[i],
			Tag:  patterns[i],
			Link: "https://example.com/" + patterns[i],
		}
		serverStr := string(server.MarshalForDashboard())
		var serverRestore Server
		if utils.Json.Unmarshal([]byte(serverStr), &serverRestore) != nil {
			t.Fatalf("Error: %s", serverStr)
		}
		if server.Name != serverRestore.Name {
			t.Fatalf("Expected %s, but got %s", server.Name, serverRestore.Name)
		}
		linkJSON, _ := utils.Json.Marshal(server.Link)
		if !strings.Contains(serverStr, `"Link": `+string(linkJSON)) {
			t.Fatalf("dashboard JSON missing link: %s", serverStr)
		}
	}
}
