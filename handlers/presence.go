package handlers

// PresenceProvider defines the contract for checking user online presence.
type PresenceProvider interface {
	IsUserOnline(userID int) bool
	GetOnlineUserIDs() []int
}
