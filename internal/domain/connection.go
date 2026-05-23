package domain

// Connection is an Airflow-style connection: credentials/endpoints for operators,
// managed from the Admin UI. Password and Extra are encrypted at rest (ADR 0019);
// Password is write-only and never returned by the API.
type Connection struct {
	ConnID      string
	ConnType    string
	Host        string
	Schema      string
	Login       string
	Password    string
	Port        *int
	Extra       string
	Description string
}
