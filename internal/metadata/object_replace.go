package metadata

import "context"

// Replace checks the current destination and stores the replacement in one write
// transaction. The returned path belongs to the version actually replaced.
func (r *sqliteObjectRepository) Replace(ctx context.Context, object *Object, ifMatch, ifNoneMatch string) (string, error) {
	tx, err := beginImmediate(ctx, r.db)
	if err != nil {
		return "", err
	}
	defer tx.close()
	existing, err := loadObjectVersion(ctx, tx.Conn, object.BucketName, object.Key)
	if err != nil {
		return "", err
	}
	if err := checkWritePreconditions(existing, ifMatch, ifNoneMatch); err != nil {
		return "", err
	}
	if err := upsertObject(ctx, tx, object); err != nil {
		return "", err
	}
	if err := tx.commit(ctx); err != nil {
		return "", err
	}
	return existing.storagePath, nil
}

func (r *cachedObjectRepository) Replace(ctx context.Context, object *Object, ifMatch, ifNoneMatch string) (string, error) {
	defer r.cache.evictObject(object.BucketName, object.Key)
	return r.delegate.Replace(ctx, object, ifMatch, ifNoneMatch)
}
