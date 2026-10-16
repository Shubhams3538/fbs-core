package metadata

import (
	"context"
	"errors"
	"fmt"
)

var ErrBucketHasMultipartUploads = errors.New("bucket has multipart uploads")

func (r *sqliteBucketRepository) DeleteWithObjects(ctx context.Context, name string) ([]Object, error) {
	tx, err := beginImmediate(ctx, r.db)
	if err != nil {
		return nil, fmt.Errorf("begin bucket deletion: %w", err)
	}
	defer tx.close()

	// Hold the database write lock from this check through bucket deletion.
	// Upload creation and completion on other connections cannot slip between.
	var hasUploads bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM multipart_uploads WHERE bucket_name = ?)`, name).Scan(&hasUploads); err != nil {
		return nil, fmt.Errorf("check bucket multipart uploads: %w", err)
	}
	if hasUploads {
		return nil, ErrBucketHasMultipartUploads
	}

	objects, err := deleteBucketObjects(ctx, tx, name)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM buckets WHERE name = ?`, name)
	if err != nil {
		return nil, fmt.Errorf("delete bucket: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("delete bucket rows affected: %w", err)
	}
	if count == 0 {
		return nil, ErrBucketNotFound
	}
	if err := tx.commit(ctx); err != nil {
		return nil, fmt.Errorf("commit bucket deletion: %w", err)
	}
	return objects, nil
}

// deleteBucketObjects captures exactly the rows removed by this transaction.
func deleteBucketObjects(ctx context.Context, tx *immediateTransaction, name string) ([]Object, error) {
	const q = `DELETE FROM objects WHERE bucket_name = ?
RETURNING id, bucket_name, key, size, etag, content_type, storage_path, created_at, updated_at,
          is_multipart, parts_count, checksum_crc32, checksum_crc32c, checksum_crc64nvme,
          checksum_sha1, checksum_sha256, user_metadata`
	rows, err := tx.QueryContext(ctx, q, name)
	if err != nil {
		return nil, fmt.Errorf("delete bucket objects: %w", err)
	}
	defer rows.Close()
	var objects []Object
	for rows.Next() {
		obj, err := scanObject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan deleted bucket object: %w", err)
		}
		objects = append(objects, *obj)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("delete bucket objects rows: %w", err)
	}
	return objects, nil
}
