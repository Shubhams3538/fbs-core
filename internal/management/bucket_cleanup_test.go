package management_test

import (
	"context"
	"testing"

	"github.com/i-got-this-faa/fbs/internal/metadata"
)

// seedBucketCleanupFiles creates real root and nested objects and returns their
// backing filenames so cleanup assertions do not depend on metadata deletion.
func seedBucketCleanupFiles(t *testing.T, env managementTestEnv) []string {
	t.Helper()

	var paths []string
	for _, key := range []string{"stored.jpg", "nested/stored.jpg"} {
		seedStoredObject(t, env, "photos", key, "stored object body")
		object, err := metadata.NewObjectRepository(env.db).GetByKey(context.Background(), "photos", key)
		if err != nil {
			t.Fatalf("load seeded object %q: %v", key, err)
		}
		file, err := env.storage.Open(context.Background(), object.StoragePath)
		if err != nil {
			t.Fatalf("open seeded backing file %q: %v", key, err)
		}
		paths = append(paths, file.Name())
		if err := file.Close(); err != nil {
			t.Fatalf("close seeded backing file: %v", err)
		}
	}
	return paths
}
