package view

// ConnectorOAuthAppConfig carries the connector module-level OAuth app
// credentials for rendering on the connector list page. This is NOT per-row
// data — one set of credentials is shared across all rows of a connector.
//
// Credentials are stored in the configs table under owner="connector_oauth"
// with keys "<connector_key>_client_id" / "<connector_key>_client_secret".
//
// ClientSecret is always masked (••••••••) when displaying — the handler
// never sends the plaintext to the template.
//
// Enabled must be true for the section to render. The handler sets it only
// for connector keys that support OAuth (currently: slack) and only for admins.
type ConnectorOAuthAppConfig struct {
	Enabled      bool   // true = render the OAuth App section
	ClientID     string // empty when not yet configured
	ClientSecret string // "••••••••" when set, empty when not
	OAuthURL     string // non-empty when ClientID is configured; links to OAuth start
}
