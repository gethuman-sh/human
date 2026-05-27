package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// GraphQLRequest is the standard GraphQL request body.
type GraphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// GraphQLResponse is the standard GraphQL response envelope.
type GraphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []GraphQLError  `json:"errors,omitempty"`
}

// GraphQLError represents a single GraphQL error.
type GraphQLError struct {
	Message string `json:"message"`
}

// DoGraphQL marshals a GraphQL request, posts it, and returns the data field.
func (c *Client) DoGraphQL(ctx context.Context, graphqlPath, query string, variables map[string]any) (json.RawMessage, error) {
	reqBody, err := json.Marshal(GraphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling graphql request")
	}

	resp, err := c.Do(ctx, "POST", graphqlPath, "", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBodyBytes+1))
	if err != nil {
		return nil, errors.WrapWithDetails(err, "reading response body")
	}
	if int64(len(body)) > MaxResponseBodyBytes {
		return nil, errors.WithDetails(
			fmt.Sprintf("graphql response body exceeds size limit of %d bytes", MaxResponseBodyBytes))
	}

	var gqlResp GraphQLResponse
	if err := json.Unmarshal(body, &gqlResp); err != nil {
		return nil, errors.WrapWithDetails(err, "decoding graphql response")
	}

	if len(gqlResp.Errors) > 0 {
		msgs := make([]string, len(gqlResp.Errors))
		for i, e := range gqlResp.Errors {
			msgs[i] = e.Message
		}
		return nil, errors.WithDetails(
			fmt.Sprintf("%s graphql error: %s", c.displayName(), strings.Join(msgs, "; ")))
	}

	return gqlResp.Data, nil
}
