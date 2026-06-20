package jira

import "encoding/json"

type searchResult struct {
	Issues []issue `json:"issues"`
}

type issue struct {
	Key    string      `json:"key"`
	Fields issueFields `json:"fields"`
}

type issueFields struct {
	Summary   string      `json:"summary"`
	Status    statusField `json:"status"`
	Updated   string      `json:"updated"`
	IssueType nameOnly    `json:"issuetype"`
}

type statusField struct {
	Name string `json:"name"`
}

type issueDetail struct {
	Key    string            `json:"key"`
	Fields issueDetailFields `json:"fields"`
}

type issueDetailFields struct {
	Summary     string          `json:"summary"`
	Status      statusField     `json:"status"`
	Priority    *nameField      `json:"priority"`
	Assignee    *nameField      `json:"assignee"`
	Reporter    *nameField      `json:"reporter"`
	Description json.RawMessage `json:"description"`
	IssueType   nameOnly        `json:"issuetype"`
	Parent      *keyField       `json:"parent"`
}

type nameField struct {
	DisplayName string `json:"displayName"`
	Name        string `json:"name"`
}

type createRequest struct {
	Fields createFields `json:"fields"`
}

type createFields struct {
	Project     keyField       `json:"project"`
	Summary     string         `json:"summary"`
	IssueType   nameOnly       `json:"issuetype"`
	Description map[string]any `json:"description,omitempty"`
	Parent      *keyField      `json:"parent,omitempty"`
}

type keyField struct {
	Key string `json:"key"`
}

type nameOnly struct {
	Name string `json:"name"`
}

type createResponse struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

type commentBody struct {
	Body map[string]any `json:"body"`
}

type jiraComment struct {
	ID      string          `json:"id"`
	Author  *nameField      `json:"author"`
	Body    json.RawMessage `json:"body"`
	Created string          `json:"created"`
}

type commentsResponse struct {
	Comments []jiraComment `json:"comments"`
}

type transitionsResponse struct {
	Transitions []jiraTransition `json:"transitions"`
}

type jiraTransition struct {
	ID   string      `json:"id"`
	Name string      `json:"name"`
	To   statusField `json:"to"`
}

type myselfResponse struct {
	AccountID string `json:"accountId"`
}
