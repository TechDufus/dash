A panel on the Storage page shows the buckets of a RustFS object store and its capacity, read from
the store's series through the prometheus plugin. A bucket at 80% of its quota is a warning on the
Overview and at 90% critical; a bucket without a quota raises nothing. It is the one plugin that
is off until `plugins.rustfs.enabled` is `true`: nothing in its settings says an object store
exists.
