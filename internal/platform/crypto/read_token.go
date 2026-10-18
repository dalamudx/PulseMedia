package crypto

type ReadTokenIssuer struct{}

func (ReadTokenIssuer) IssueReadToken() (string, []byte, error) { return GenerateToken("pm_api_") }
