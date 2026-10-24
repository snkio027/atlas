// Offline OT-0 artifact verification; this program never imports or pulls images.
package main

import (
	"encoding/json"
	"fmt"
	"github.com/snkio027/atlas/internal/oci"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) != 3 {
		panic("usage: check_images <versions.lock.json> <image-cache-directory>")
	}
	b, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	var lock map[string]json.RawMessage
	if err := json.Unmarshal(b, &lock); err != nil {
		panic(err)
	}
	for _, key := range []string{"nodeImage", "argoImage", "redisImage"} {
		var image string
		if err := json.Unmarshal(lock[key], &image); err != nil {
			panic(err)
		}
		_, digest, ok := strings.Cut(image, "@sha256:")
		if !ok {
			panic("image digest missing")
		}
		if err := oci.Verify(filepath.Join(os.Args[2], digest+".tar"), image); err != nil {
			panic(err)
		}
		fmt.Println(key + ": verified locked OCI closure")
	}
}
