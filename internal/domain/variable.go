package domain

// Variable is a tenant-scoped key/value setting consumed by DAGs and managed
// from the Admin UI. Value is stored as-is (plaintext for the MVP); the API
// masks values of secret-ish keys.
type Variable struct {
	Key         string
	Value       string
	Description string
}
