package tvdb

import "context"

// ProbeDiagnosticAuthentication performs one native login without retaining its token.
func (c *Client) ProbeDiagnosticAuthentication(ctx context.Context) error {
	_, err := c.login(ctx)
	return err
}
