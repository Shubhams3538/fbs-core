package metadata

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/i-got-this-faa/fbs/migrations"
)

func TestLegacyMultipartUpgradeRemainsReadable(t *testing.T) {
	db := openTestDB(t)
	bucket := insertTestBucket(t, db)
	// Recreate the version-10 shape, then run the real upgrade, including its NULL
	// values. Also covers databases already upgraded before this scanner fix.
	for _, query := range []string{
		`ALTER TABLE multipart_uploads DROP COLUMN checksum_algorithm`,
		`ALTER TABLE multipart_uploads DROP COLUMN user_metadata`,
		`ALTER TABLE objects DROP COLUMN parts_count`,
		`ALTER TABLE objects DROP COLUMN user_metadata`,
		`DROP TABLE share_links`,
		`DELETE FROM schema_migrations WHERE version > 10`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO multipart_uploads(id,bucket_name,key,content_type,status) VALUES ('legacy',?,'key','text/plain','active')`, bucket); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(db); err != nil {
		t.Fatal(err)
	}
	repo := NewMultipartUploadRepository(db)
	ctx := context.Background()
	upload, err := repo.GetByID(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if upload.ChecksumAlgorithm != "" || upload.UserMetadata != nil {
		t.Fatalf("legacy defaults: %+v", upload)
	}
	listed, _, _, _, err := repo.ListByBucket(ctx, bucket, "", "", "", 1000)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list: %+v, %v", listed, err)
	}
	stale, err := repo.ListStale(ctx, time.Now().Add(time.Hour))
	if err != nil || len(stale) != 1 {
		t.Fatalf("stale: %+v, %v", stale, err)
	}
	if err := repo.ClaimUpload(ctx, upload.ID, MultipartUploadStatusAborted); err != nil {
		t.Fatalf("legacy upload cannot be cleaned: %v", err)
	}
	if err := repo.Delete(ctx, upload.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDelimitedListingUnicodePrefixesAndDelimiters(t *testing.T) {
	for _, test := range []struct{ prefix, delimiter string }{{"é/", "/"}, {"照片/", "/"}, {"📁/", "/"}, {"é/", "::"}, {"写真/", "📁"}} {
		t.Run(test.prefix+test.delimiter, func(t *testing.T) {
			db := openTestDB(t)
			bucket := insertTestBucket(t, db)
			repo := NewObjectRepository(db)
			ctx := context.Background()
			keys := []string{test.prefix + "a", test.prefix + "b" + test.delimiter + "child1", test.prefix + "b" + test.delimiter + "child2", test.prefix + "c", "unrelated"}
			for _, key := range keys {
				if err := repo.Create(ctx, testCacheObject(bucket, key, "etag")); err != nil {
					t.Fatal(err)
				}
			}
			entries, err := repo.ListDelimited(ctx, bucket, test.prefix, "", test.delimiter, 10)
			if err != nil {
				t.Fatal(err)
			}
			var virtual []string
			for _, entry := range entries {
				virtual = append(virtual, entry.VirtualKey)
			}
			want := []string{test.prefix + "a", test.prefix + "b" + test.delimiter, test.prefix + "c"}
			if !reflect.DeepEqual(virtual, want) {
				t.Fatalf("entries=%q, want %q", virtual, want)
			}
			if !entries[1].IsPrefix || entries[1].CursorKey != keys[2] || entries[1].Object != nil {
				t.Fatalf("prefix grouping: %+v", entries[1])
			}
			first, err := repo.ListDelimited(ctx, bucket, test.prefix, "", test.delimiter, 1)
			if err != nil || len(first) != 2 {
				t.Fatalf("lookahead=%+v error=%v", first, err)
			}
			rest, err := repo.ListDelimited(ctx, bucket, test.prefix, entries[1].CursorKey, test.delimiter, 10)
			if err != nil || len(rest) != 1 || rest[0].VirtualKey != want[2] {
				t.Fatalf("cursor page=%+v error=%v", rest, err)
			}
		})
	}
}
