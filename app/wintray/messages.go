//go:build windows

package wintray

const (
	firstTimeTitle   = AppName + " is running" // xollama-hook: app-brand
	firstTimeMessage = "Click here to get started"
	updateTitle      = "Update available"
	updateMessage    = AppName + " version %s is ready to install" // xollama-hook: app-brand

	quitMenuTitle            = "Quit " + AppName // xollama-hook: app-brand
	updateAvailableMenuTitle = "An update is available"
	updateMenuTitle          = "Restart to update"
	diagLogsMenuTitle        = "View logs"
	openAppsMenuTitle        = "Open " + AppName // xollama-hook: app-brand
	settingsUIMenuTitle      = "Settings"
)
