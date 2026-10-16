package metadata

// A cache-wide revision invalidates in-flight misses without retaining a version
// map for every key ever requested. Writes evict after committing; they never
// publish caller-provided values that may already have been superseded.
func (c *MetadataCache) currentRevision() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revision
}

func (c *MetadataCache) publishBucket(bucket *Bucket, revision uint64) bool {
	if c == nil || c.maxBytes <= 0 {
		return true
	}
	return c.publish(cacheEntry{
		key: bucketKey(bucket.Name), value: cacheValue{bucket: cloneBucket(bucket)},
		size: estimateBucketSize(bucket),
	}, revision)
}

func (c *MetadataCache) publishObject(object *Object, revision uint64) bool {
	if c == nil || c.maxBytes <= 0 {
		return true
	}
	return c.publish(cacheEntry{
		key: objectKey(object.BucketName, object.Key), value: cacheValue{object: cloneObject(object)},
		size: estimateObjectSize(object),
	}, revision)
}

func (c *MetadataCache) publish(entry cacheEntry, revision uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revision != revision {
		return false
	}
	c.putLocked(entry)
	return true
}
