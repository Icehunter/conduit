package providerauth

// BuiltinConfigs returns the built-in provider auth configurations for
// catalog providers that use API-key auth. These are the providers that
// C-O3 supports out of the box; additional providers can be added later.
func BuiltinConfigs() []Config {
	return []Config{
		{
			ID:          "openai",
			DisplayName: "OpenAI",
			DocsURL:     "https://platform.openai.com/api-keys",
			Methods: []Method{
				{
					Kind:   MethodAPIKey,
					Label:  "Enter API key",
					EnvVar: "OPENAI_API_KEY",
					Hint:   "sk-...",
				},
			},
		},
		{
			ID:          "gemini",
			DisplayName: "Gemini (Google AI Studio)",
			DocsURL:     "https://aistudio.google.com/app/apikey",
			Methods: []Method{
				{
					Kind:   MethodAPIKey,
					Label:  "Enter API key",
					EnvVar: "GEMINI_API_KEY",
					Hint:   "AIza...",
				},
			},
		},
		{
			ID:          "openrouter",
			DisplayName: "OpenRouter",
			DocsURL:     "https://openrouter.ai/settings/keys",
			Methods: []Method{
				{
					Kind:   MethodAPIKey,
					Label:  "Enter API key",
					EnvVar: "OPENROUTER_API_KEY",
					Hint:   "sk-or-...",
				},
			},
		},
	}
}

// BuiltinByID returns the Config for the given provider ID, or false if not found.
func BuiltinByID(id string) (Config, bool) {
	for _, c := range BuiltinConfigs() {
		if c.ID == id {
			return c, true
		}
	}
	return Config{}, false
}
