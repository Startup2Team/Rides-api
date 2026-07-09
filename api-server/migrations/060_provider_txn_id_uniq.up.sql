CREATE UNIQUE INDEX IF NOT EXISTS idx_package_purchases_provider_txn_id
ON package_purchases(provider_txn_id)
WHERE provider_txn_id IS NOT NULL AND provider_txn_id <> '';
