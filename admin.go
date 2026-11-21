package main

func (a *App) IsAdmin(telegramID int64) bool {
	return a.Config.AdminIDs[telegramID]
}

// Admin commands and configurable marketplace settings will be implemented
// in the Admin module phase.
