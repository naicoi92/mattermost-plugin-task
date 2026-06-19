package kvstore

// ListUserAssignedTaskIDs returns the IDs of tasks assigned to userID, scanning
// the idx:u:{userID}:assigned: index.
func (c Client) ListUserAssignedTaskIDs(userID string) ([]string, error) {
	if userID == "" {
		return nil, nil
	}
	return c.ListTaskIDsByPrefix(keyUserPrefix + userID + keyAssignedInfix)
}

// ListUserCreatedTaskIDs returns the IDs of tasks created by userID, scanning
// the idx:u:{userID}:created: index.
func (c Client) ListUserCreatedTaskIDs(userID string) ([]string, error) {
	if userID == "" {
		return nil, nil
	}
	return c.ListTaskIDsByPrefix(keyUserPrefix + userID + keyCreatedInfix)
}

// ListChannelTaskIDs returns the IDs of tasks belonging to channelID, scanning
// the idx:ch:{channelID}:task: index.
func (c Client) ListChannelTaskIDs(channelID string) ([]string, error) {
	if channelID == "" {
		return nil, nil
	}
	return c.ListTaskIDsByPrefix(keyChannelPrefix + channelID + keyChannelInfix)
}

// ListAllTaskIDs returns the IDs of all known tasks, scanning the global
// idx:all:task: index.
func (c Client) ListAllTaskIDs() ([]string, error) {
	return c.ListTaskIDsByPrefix(keyAllTasksPrefix)
}
