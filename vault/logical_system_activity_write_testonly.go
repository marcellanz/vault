// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

//go:build testonly

package vault

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-uuid"
	"github.com/hashicorp/vault/helper/namespace"
	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
	"github.com/hashicorp/vault/vault/activity"
	"github.com/hashicorp/vault/vault/activity/generation"
	"google.golang.org/protobuf/encoding/protojson"
)

const helpText = "Create activity log data for testing purposes"

func (b *SystemBackend) activityWritePath() *framework.Path {
	return &framework.Path{
		Pattern:         "internal/counters/activity/write$",
		HelpDescription: helpText,
		HelpSynopsis:    helpText,
		Fields: map[string]*framework.FieldSchema{
			"input": {
				Type:        framework.TypeString,
				Description: "JSON input for generating mock data",
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.CreateOperation: &framework.PathOperation{
				Callback: b.handleActivityWriteData,
				Summary:  "Write activity log data",
			},
		},
	}
}

func (b *SystemBackend) handleActivityWriteData(ctx context.Context, request *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	json := data.Get("input")
	input := &generation.ActivityLogMockInput{}
	err := protojson.Unmarshal([]byte(json.(string)), input)
	if err != nil {
		return logical.ErrorResponse("Invalid input data: %s", err), logical.ErrInvalidRequest
	}
	if len(input.Write) == 0 {
		return logical.ErrorResponse("Missing required \"write\" values"), logical.ErrInvalidRequest
	}
	if len(input.Data) == 0 {
		return logical.ErrorResponse("Missing required \"data\" values"), logical.ErrInvalidRequest
	}
	return nil, nil
}

// singleMonthActivityClients holds a single month's client IDs, in the order they were seen
type singleMonthActivityClients struct {
	// clients are indexed by ID
	clients []*activity.EntityRecord
	// predefinedSegments map from the segment number to the client's index in
	// the clients slice
	predefinedSegments map[int][]int
	// generationParameters holds the generation request
	generationParameters *generation.Data
}

// multipleMonthsActivityClients holds multiple month's data
type multipleMonthsActivityClients struct {
	// months are in order, with month 0 being the current month and index 1 being 1 month ago
	months []*singleMonthActivityClients
}

func (s *singleMonthActivityClients) addEntityRecord(record *activity.EntityRecord, segmentIndex *int) {
	s.clients = append(s.clients, record)
	if segmentIndex != nil {
		index := len(s.clients) - 1
		s.predefinedSegments[*segmentIndex] = append(s.predefinedSegments[*segmentIndex], index)
	}
}

// populateSegments converts a month of clients into a segmented map. The map's
// keys are the segment index, and the value are the clients that were seen in
// that index. If the value is an empty slice, then it's an empty index. If the
// value is nil, then it's a skipped index
func (s *singleMonthActivityClients) populateSegments() (map[int][]*activity.EntityRecord, error) {
	segments := make(map[int][]*activity.EntityRecord)
	ignoreIndexes := make(map[int]struct{})
	skipIndexes := s.generationParameters.SkipSegmentIndexes
	emptyIndexes := s.generationParameters.EmptySegmentIndexes

	for _, i := range skipIndexes {
		segments[int(i)] = nil
		ignoreIndexes[int(i)] = struct{}{}
	}
	for _, i := range emptyIndexes {
		segments[int(i)] = make([]*activity.EntityRecord, 0, 0)
		ignoreIndexes[int(i)] = struct{}{}
	}

	// if we have predefined segments, then we can construct the map using those
	if len(s.predefinedSegments) > 0 {
		for segment, clientIndexes := range s.predefinedSegments {
			clientsInSegment := make([]*activity.EntityRecord, 0, len(clientIndexes))
			for _, idx := range clientIndexes {
				clientsInSegment = append(clientsInSegment, s.clients[idx])
			}
			segments[segment] = clientsInSegment
		}
		return segments, nil
	}

	totalSegmentCount := 1
	if s.generationParameters.GetNumSegments() > 0 {
		totalSegmentCount = int(s.generationParameters.GetNumSegments())
	}
	numNonUsable := len(skipIndexes) + len(emptyIndexes)
	usableSegmentCount := totalSegmentCount - numNonUsable
	if usableSegmentCount <= 0 {
		return nil, fmt.Errorf("num segments %d is too low, it must be greater than %d (%d skipped indexes + %d empty indexes)", totalSegmentCount, numNonUsable, len(skipIndexes), len(emptyIndexes))
	}

	// determine how many clients should be in each segment
	segmentSizes := len(s.clients) / usableSegmentCount
	if len(s.clients)%usableSegmentCount != 0 {
		segmentSizes++
	}

	clientIndex := 0
	for i := 0; i < totalSegmentCount; i++ {
		if clientIndex >= len(s.clients) {
			break
		}
		if _, ok := ignoreIndexes[i]; ok {
			continue
		}
		for len(segments[i]) < segmentSizes && clientIndex < len(s.clients) {
			segments[i] = append(segments[i], s.clients[clientIndex])
			clientIndex++
		}
	}
	return segments, nil
}

// addNewClients generates clients according to the given parameters, and adds them to the month
// the client will always have the mountAccessor as its mount accessor
func (s *singleMonthActivityClients) addNewClients(c *generation.Client, mountAccessor string, segmentIndex *int) error {
	count := 1
	if c.Count > 1 {
		count = int(c.Count)
	}
	clientType := entityActivityType
	if c.NonEntity {
		clientType = nonEntityTokenActivityType
	}
	for i := 0; i < count; i++ {
		record := &activity.EntityRecord{
			ClientID:      c.Id,
			NamespaceID:   c.Namespace,
			NonEntity:     c.NonEntity,
			MountAccessor: mountAccessor,
			ClientType:    clientType,
		}
		if record.ClientID == "" {
			var err error
			record.ClientID, err = uuid.GenerateUUID()
			if err != nil {
				return err
			}
		}
		s.addEntityRecord(record, segmentIndex)
	}
	return nil
}

// processMonth populates a month of client data
func (m *multipleMonthsActivityClients) processMonth(ctx context.Context, core *Core, month *generation.Data) error {
	// default to using the root namespace and the first mount on the root namespace
	mounts, err := core.ListMounts()
	if err != nil {
		return err
	}
	defaultMountAccessorRootNS := ""
	for _, mount := range mounts {
		if mount.NamespaceID == namespace.RootNamespaceID {
			defaultMountAccessorRootNS = mount.Accessor
			break
		}
	}
	m.months[month.GetMonthsAgo()].generationParameters = month
	add := func(c []*generation.Client, segmentIndex *int) error {
		for _, clients := range c {

			if clients.Namespace == "" {
				clients.Namespace = namespace.RootNamespaceID
			}

			// verify that the namespace exists
			ns, err := core.NamespaceByID(ctx, clients.Namespace)
			if err != nil {
				return err
			}

			// verify that the mount exists
			if clients.Mount != "" {
				nctx := namespace.ContextWithNamespace(ctx, ns)
				mountEntry := core.router.MatchingMountEntry(nctx, clients.Mount)
				if mountEntry == nil {
					return fmt.Errorf("unable to find matching mount in namespace %s", clients.Namespace)
				}
			}

			mountAccessor := defaultMountAccessorRootNS
			if clients.Namespace != namespace.RootNamespaceID && clients.Mount == "" {
				// if we're not using the root namespace, find a mount on the namespace that we are using
				found := false
				for _, mount := range mounts {
					if mount.NamespaceID == clients.Namespace {
						mountAccessor = mount.Accessor
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("unable to find matching mount in namespace %s", clients.Namespace)
				}
			}

			err = m.addClientToMonth(month.GetMonthsAgo(), clients, mountAccessor, segmentIndex)
			if err != nil {
				return err
			}
		}
		return nil
	}

	if month.GetAll() != nil {
		return add(month.GetAll().GetClients(), nil)
	}
	predefinedSegments := month.GetSegments()
	for i, segment := range predefinedSegments.GetSegments() {
		index := i
		if segment.SegmentIndex != nil {
			index = int(*segment.SegmentIndex)
		}
		err = add(segment.GetClients().GetClients(), &index)
		if err != nil {
			return err
		}
	}

	return nil
}

func (m *multipleMonthsActivityClients) addClientToMonth(monthsAgo int32, c *generation.Client, mountAccessor string, segmentIndex *int) error {
	if c.Repeated || c.RepeatedFromMonth > 0 {
		return m.addRepeatedClients(monthsAgo, c, mountAccessor, segmentIndex)
	}
	return m.months[monthsAgo].addNewClients(c, mountAccessor, segmentIndex)
}

func (m *multipleMonthsActivityClients) addRepeatedClients(monthsAgo int32, c *generation.Client, mountAccessor string, segmentIndex *int) error {
	addingTo := m.months[monthsAgo]
	repeatedFromMonth := monthsAgo + 1
	if c.RepeatedFromMonth > 0 {
		repeatedFromMonth = c.RepeatedFromMonth
	}
	repeatedFrom := m.months[repeatedFromMonth]
	numClients := 1
	if c.Count > 0 {
		numClients = int(c.Count)
	}
	for _, client := range repeatedFrom.clients {
		if c.NonEntity == client.NonEntity && mountAccessor == client.MountAccessor && c.Namespace == client.NamespaceID {
			addingTo.addEntityRecord(client, segmentIndex)
			numClients--
			if numClients == 0 {
				break
			}
		}
	}
	if numClients > 0 {
		return fmt.Errorf("missing repeated %d clients matching given parameters", numClients)
	}
	return nil
}

func newMultipleMonthsActivityClients(numberOfMonths int) *multipleMonthsActivityClients {
	m := &multipleMonthsActivityClients{
		months: make([]*singleMonthActivityClients, numberOfMonths),
	}
	for i := 0; i < numberOfMonths; i++ {
		m.months[i] = &singleMonthActivityClients{
			predefinedSegments: make(map[int][]int),
		}
	}
	return m
}
