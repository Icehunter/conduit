package brave

// Available reports whether an API key is configured.
func (p *Provider) Available() bool {
	_, err := p.resolveKey()
	return err == nil
}
