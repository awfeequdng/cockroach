// Copyright 2015 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.
//
// Author: Marc Berhault (marc@cockroachlabs.com)

package sqlbase

import (
	"fmt"

	"github.com/cockroachdb/cockroach/pkg/config"
	"github.com/cockroachdb/cockroach/pkg/keys"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/security"
	"github.com/cockroachdb/cockroach/pkg/sql/privilege"
)

// sql CREATE commands and full schema for each system table.
// These strings are *not* used at runtime, but are checked by the
// `TestSystemTableLiterals` test that compares the table generated by
// evaluating the `CREATE TABLE` statement to the descriptor literal that is
// actually used at runtime.

// These system tables are part of the system config.
const (
	NamespaceTableSchema = `
CREATE TABLE system.namespace (
  "parentID" INT,
  name       STRING,
  id         INT,
  PRIMARY KEY ("parentID", name)
);`

	DescriptorTableSchema = `
CREATE TABLE system.descriptor (
  id         INT PRIMARY KEY,
  descriptor BYTES
);`

	UsersTableSchema = `
CREATE TABLE system.users (
  username         STRING PRIMARY KEY,
  "hashedPassword" BYTES
);`

	// Zone settings per DB/Table.
	ZonesTableSchema = `
CREATE TABLE system.zones (
  id     INT PRIMARY KEY,
  config BYTES
);`

	SettingsTableSchema = `
CREATE TABLE system.settings (
	name              STRING    NOT NULL PRIMARY KEY,
	value             STRING    NOT NULL,
	"lastUpdated"     TIMESTAMP NOT NULL DEFAULT now(),
	"valueType"       STRING,
	FAMILY (name, value, "lastUpdated", "valueType")
);`
)

// These system tables are not part of the system config.
const (
	LeaseTableSchema = `
CREATE TABLE system.lease (
  "descID"   INT,
  version    INT,
  "nodeID"   INT,
  expiration TIMESTAMP,
  PRIMARY KEY ("descID", version, expiration, "nodeID")
);`

	EventLogTableSchema = `
CREATE TABLE system.eventlog (
  timestamp     TIMESTAMP  NOT NULL,
  "eventType"   STRING     NOT NULL,
  "targetID"    INT        NOT NULL,
  "reportingID" INT        NOT NULL,
  info          STRING,
  "uniqueID"    BYTES      DEFAULT uuid_v4(),
  PRIMARY KEY (timestamp, "uniqueID")
);`

	// rangelog is currently envisioned as a wide table; many different event
	// types can be recorded to the table.
	RangeEventTableSchema = `
CREATE TABLE system.rangelog (
  timestamp      TIMESTAMP  NOT NULL,
  "rangeID"      INT        NOT NULL,
  "storeID"      INT        NOT NULL,
  "eventType"    STRING     NOT NULL,
  "otherRangeID" INT,
  info           STRING,
  "uniqueID"     INT        DEFAULT unique_rowid(),
  PRIMARY KEY (timestamp, "uniqueID")
);`

	UITableSchema = `
CREATE TABLE system.ui (
	key           STRING PRIMARY KEY,
	value         BYTES,
	"lastUpdated" TIMESTAMP NOT NULL
);`

	JobsTableSchema = `
CREATE TABLE system.jobs (
	id                INT       DEFAULT unique_rowid() PRIMARY KEY,
	status            STRING    NOT NULL,
	created           TIMESTAMP NOT NULL DEFAULT now(),
	payload           BYTES     NOT NULL,
	INDEX (status, created),
	FAMILY (id, status, created, payload)
);`
)

func pk(name string) IndexDescriptor {
	return IndexDescriptor{
		Name:             "primary",
		ID:               1,
		Unique:           true,
		ColumnNames:      []string{name},
		ColumnDirections: singleASC,
		ColumnIDs:        singleID1,
	}
}

// SystemAllowedPrivileges describes the allowable privilege lists for each
// system object. The root user must have the privileges of exactly one of those
// privilege lists. No user may have more privileges than the root user.
//
// Some system objects were created with different privileges in previous
// versions of the system. These privileges are no longer "desired" but must
// still be "allowed," or this version and the previous version with different
// privileges will be unable to coexist in the same cluster because one version
// will think it has invalid table descriptors.
//
// The currently-desired privileges (i.e., the privileges with which the object
// will be created in fresh clusters) must be listed first in the mapped value,
// followed by previously-desirable but still allowable privileges in any order.
//
// If we supported backwards-incompatible migrations, pruning allowed privileges
// would require a two-step migration process. First, a new version that allows
// both must be deployed to all nodes, after which a a migration to upgrade from
// the old privileges to the new privileges can be run. Only then can a version
// that removes the old allowed versions be deployed. TODO(benesch): Once we
// support backwards-incompatible migrations, prune old allowed privileges.
var SystemAllowedPrivileges = map[ID]privilege.Lists{
	keys.SystemDatabaseID:  {privilege.ReadData},
	keys.NamespaceTableID:  {privilege.ReadData},
	keys.DescriptorTableID: {privilege.ReadData},
	keys.UsersTableID:      {privilege.ReadWriteData},
	keys.ZonesTableID:      {privilege.ReadWriteData},
	// We eventually want to migrate the table to appear read-only to force the
	// the use of a validating, logging accessor, so we'll go ahead and tolerate
	// read-only privs to make that migration possible later.
	keys.SettingsTableID:   {privilege.ReadWriteData, privilege.ReadData},
	keys.LeaseTableID:      {privilege.ReadWriteData, {privilege.ALL}},
	keys.EventLogTableID:   {privilege.ReadWriteData, {privilege.ALL}},
	keys.RangeEventTableID: {privilege.ReadWriteData, {privilege.ALL}},
	keys.UITableID:         {privilege.ReadWriteData, {privilege.ALL}},
	// IMPORTANT: CREATE|DROP|ALL privileges should always be denied or database
	// users will be able to modify system tables' schemas at will. CREATE and
	// DROP privileges are allowed on the above system tables for backwards
	// compatibility reasons only!
	keys.JobsTableID: {privilege.ReadWriteData},
}

// SystemDesiredPrivileges returns the desired privilege list (i.e., the
// privilege list with which the object should be created with in a fresh
// cluster) for a given system object ID. This function panics if id does not
// exist in the SystemAllowedPrivileges map and should only be used in contexts
// where id is guaranteed to exist.
func SystemDesiredPrivileges(id ID) privilege.List {
	return SystemAllowedPrivileges[id][0]
}

// Helpers used to make some of the TableDescriptor literals below more concise.
var (
	colTypeInt       = ColumnType{SemanticType: ColumnType_INT}
	colTypeString    = ColumnType{SemanticType: ColumnType_STRING}
	colTypeBytes     = ColumnType{SemanticType: ColumnType_BYTES}
	colTypeTimestamp = ColumnType{SemanticType: ColumnType_TIMESTAMP}
	singleASC        = []IndexDescriptor_Direction{IndexDescriptor_ASC}
	singleID1        = []ColumnID{1}
)

// These system config TableDescriptor literals should match the descriptor
// that would be produced by evaluating one of the above `CREATE TABLE`
// statements. See the `TestSystemTableLiterals` which checks that they do
// indeed match, and has suggestions on writing and maintaining them.
var (
	// SystemDB is the descriptor for the system database.
	SystemDB = DatabaseDescriptor{
		Name: "system",
		ID:   keys.SystemDatabaseID,
		// Assign max privileges to root user.
		Privileges: NewPrivilegeDescriptor(security.RootUser,
			SystemDesiredPrivileges(keys.SystemDatabaseID)),
	}

	// NamespaceTable is the descriptor for the namespace table.
	NamespaceTable = TableDescriptor{
		Name:     "namespace",
		ID:       keys.NamespaceTableID,
		ParentID: 1,
		Version:  1,
		Columns: []ColumnDescriptor{
			{Name: "parentID", ID: 1, Type: colTypeInt},
			{Name: "name", ID: 2, Type: colTypeString},
			{Name: "id", ID: 3, Type: colTypeInt, Nullable: true},
		},
		NextColumnID: 4,
		Families: []ColumnFamilyDescriptor{
			{Name: "primary", ID: 0, ColumnNames: []string{"parentID", "name"}, ColumnIDs: []ColumnID{1, 2}},
			{Name: "fam_3_id", ID: 3, ColumnNames: []string{"id"}, ColumnIDs: []ColumnID{3}, DefaultColumnID: 3},
		},
		NextFamilyID: 4,
		PrimaryIndex: IndexDescriptor{
			Name:             "primary",
			ID:               1,
			Unique:           true,
			ColumnNames:      []string{"parentID", "name"},
			ColumnDirections: []IndexDescriptor_Direction{IndexDescriptor_ASC, IndexDescriptor_ASC},
			ColumnIDs:        []ColumnID{1, 2},
		},
		NextIndexID:    2,
		Privileges:     NewPrivilegeDescriptor(security.RootUser, SystemDesiredPrivileges(keys.NamespaceTableID)),
		FormatVersion:  InterleavedFormatVersion,
		NextMutationID: 1,
	}

	// DescriptorTable is the descriptor for the descriptor table.
	DescriptorTable = TableDescriptor{
		Name:       "descriptor",
		ID:         keys.DescriptorTableID,
		Privileges: NewPrivilegeDescriptor(security.RootUser, SystemDesiredPrivileges(keys.DescriptorTableID)),
		ParentID:   1,
		Version:    1,
		Columns: []ColumnDescriptor{
			{Name: "id", ID: 1, Type: colTypeInt},
			{Name: "descriptor", ID: 2, Type: colTypeBytes, Nullable: true},
		},
		NextColumnID: 3,
		Families: []ColumnFamilyDescriptor{
			{Name: "primary", ID: 0, ColumnNames: []string{"id"}, ColumnIDs: singleID1},
			{Name: "fam_2_descriptor", ID: 2, ColumnNames: []string{"descriptor"}, ColumnIDs: []ColumnID{2}, DefaultColumnID: 2},
		},
		PrimaryIndex:   pk("id"),
		NextFamilyID:   3,
		NextIndexID:    2,
		FormatVersion:  InterleavedFormatVersion,
		NextMutationID: 1,
	}

	// UsersTable is the descriptor for the users table.
	UsersTable = TableDescriptor{
		Name:     "users",
		ID:       keys.UsersTableID,
		ParentID: 1,
		Version:  1,
		Columns: []ColumnDescriptor{
			{Name: "username", ID: 1, Type: colTypeString},
			{Name: "hashedPassword", ID: 2, Type: colTypeBytes, Nullable: true},
		},
		NextColumnID: 3,
		Families: []ColumnFamilyDescriptor{
			{Name: "primary", ID: 0, ColumnNames: []string{"username"}, ColumnIDs: singleID1},
			{Name: "fam_2_hashedPassword", ID: 2, ColumnNames: []string{"hashedPassword"}, ColumnIDs: []ColumnID{2}, DefaultColumnID: 2},
		},
		PrimaryIndex:   pk("username"),
		NextFamilyID:   3,
		NextIndexID:    2,
		Privileges:     NewPrivilegeDescriptor(security.RootUser, SystemDesiredPrivileges(keys.UsersTableID)),
		FormatVersion:  InterleavedFormatVersion,
		NextMutationID: 1,
	}

	// ZonesTable is the descriptor for the zones table.
	ZonesTable = TableDescriptor{
		Name:     "zones",
		ID:       keys.ZonesTableID,
		ParentID: 1,
		Version:  1,
		Columns: []ColumnDescriptor{
			{Name: "id", ID: 1, Type: colTypeInt},
			{Name: "config", ID: 2, Type: colTypeBytes, Nullable: true},
		},
		NextColumnID: 3,
		Families: []ColumnFamilyDescriptor{
			{Name: "primary", ID: 0, ColumnNames: []string{"id"}, ColumnIDs: singleID1},
			{Name: "fam_2_config", ID: 2, ColumnNames: []string{"config"}, ColumnIDs: []ColumnID{2}, DefaultColumnID: 2},
		},
		PrimaryIndex:   pk("id"),
		NextFamilyID:   3,
		NextIndexID:    2,
		Privileges:     NewPrivilegeDescriptor(security.RootUser, SystemDesiredPrivileges(keys.ZonesTableID)),
		FormatVersion:  InterleavedFormatVersion,
		NextMutationID: 1,
	}
	// SettingsTable is the descriptor for the jobs table.
	SettingsTable = TableDescriptor{
		Name:     "settings",
		ID:       keys.SettingsTableID,
		ParentID: 1,
		Version:  1,
		Columns: []ColumnDescriptor{
			{Name: "name", ID: 1, Type: colTypeString},
			{Name: "value", ID: 2, Type: colTypeString},
			{Name: "lastUpdated", ID: 3, Type: colTypeTimestamp, DefaultExpr: &nowString},
			{Name: "valueType", ID: 4, Type: colTypeString, Nullable: true},
		},
		NextColumnID: 5,
		Families: []ColumnFamilyDescriptor{
			{
				Name:        "fam_0_name_value_lastUpdated_valueType",
				ID:          0,
				ColumnNames: []string{"name", "value", "lastUpdated", "valueType"},
				ColumnIDs:   []ColumnID{1, 2, 3, 4},
			},
		},
		NextFamilyID:   1,
		PrimaryIndex:   pk("name"),
		NextIndexID:    2,
		Privileges:     NewPrivilegeDescriptor(security.RootUser, SystemDesiredPrivileges(keys.SettingsTableID)),
		FormatVersion:  InterleavedFormatVersion,
		NextMutationID: 1,
	}
)

// These system TableDescriptor literals should match the descriptor that
// would be produced by evaluating one of the above `CREATE TABLE` statements
// for system tables that are not system config tables. See the
// `TestSystemTableLiterals` which checks that they do indeed match, and has
// suggestions on writing and maintaining them.
var (
	// LeaseTable is the descriptor for the leases table.
	LeaseTable = TableDescriptor{
		Name:     "lease",
		ID:       keys.LeaseTableID,
		ParentID: 1,
		Version:  1,
		Columns: []ColumnDescriptor{
			{Name: "descID", ID: 1, Type: colTypeInt},
			{Name: "version", ID: 2, Type: colTypeInt},
			{Name: "nodeID", ID: 3, Type: colTypeInt},
			{Name: "expiration", ID: 4, Type: colTypeTimestamp},
		},
		NextColumnID: 5,
		Families: []ColumnFamilyDescriptor{
			{Name: "primary", ID: 0, ColumnNames: []string{"descID", "version", "nodeID", "expiration"}, ColumnIDs: []ColumnID{1, 2, 3, 4}},
		},
		PrimaryIndex: IndexDescriptor{
			Name:             "primary",
			ID:               1,
			Unique:           true,
			ColumnNames:      []string{"descID", "version", "expiration", "nodeID"},
			ColumnDirections: []IndexDescriptor_Direction{IndexDescriptor_ASC, IndexDescriptor_ASC, IndexDescriptor_ASC, IndexDescriptor_ASC},
			ColumnIDs:        []ColumnID{1, 2, 4, 3},
		},
		NextFamilyID:   1,
		NextIndexID:    2,
		Privileges:     NewPrivilegeDescriptor(security.RootUser, SystemDesiredPrivileges(keys.LeaseTableID)),
		FormatVersion:  InterleavedFormatVersion,
		NextMutationID: 1,
	}

	uuidV4String = "uuid_v4()"

	// EventLogTable is the descriptor for the event log table.
	EventLogTable = TableDescriptor{
		Name:     "eventlog",
		ID:       keys.EventLogTableID,
		ParentID: 1,
		Version:  1,
		Columns: []ColumnDescriptor{
			{Name: "timestamp", ID: 1, Type: colTypeTimestamp},
			{Name: "eventType", ID: 2, Type: colTypeString},
			{Name: "targetID", ID: 3, Type: colTypeInt},
			{Name: "reportingID", ID: 4, Type: colTypeInt},
			{Name: "info", ID: 5, Type: colTypeString, Nullable: true},
			{Name: "uniqueID", ID: 6, Type: colTypeBytes, DefaultExpr: &uuidV4String},
		},
		NextColumnID: 7,
		Families: []ColumnFamilyDescriptor{
			{Name: "primary", ID: 0, ColumnNames: []string{"timestamp", "uniqueID"}, ColumnIDs: []ColumnID{1, 6}},
			{Name: "fam_2_eventType", ID: 2, ColumnNames: []string{"eventType"}, ColumnIDs: []ColumnID{2}, DefaultColumnID: 2},
			{Name: "fam_3_targetID", ID: 3, ColumnNames: []string{"targetID"}, ColumnIDs: []ColumnID{3}, DefaultColumnID: 3},
			{Name: "fam_4_reportingID", ID: 4, ColumnNames: []string{"reportingID"}, ColumnIDs: []ColumnID{4}, DefaultColumnID: 4},
			{Name: "fam_5_info", ID: 5, ColumnNames: []string{"info"}, ColumnIDs: []ColumnID{5}, DefaultColumnID: 5},
		},
		PrimaryIndex: IndexDescriptor{
			Name:             "primary",
			ID:               1,
			Unique:           true,
			ColumnNames:      []string{"timestamp", "uniqueID"},
			ColumnDirections: []IndexDescriptor_Direction{IndexDescriptor_ASC, IndexDescriptor_ASC},
			ColumnIDs:        []ColumnID{1, 6},
		},
		NextFamilyID:   6,
		NextIndexID:    2,
		Privileges:     NewPrivilegeDescriptor(security.RootUser, SystemDesiredPrivileges(keys.EventLogTableID)),
		FormatVersion:  InterleavedFormatVersion,
		NextMutationID: 1,
	}

	uniqueRowIDString = "unique_rowid()"

	// RangeEventTable is the descriptor for the range log table.
	RangeEventTable = TableDescriptor{
		Name:     "rangelog",
		ID:       keys.RangeEventTableID,
		ParentID: 1,
		Version:  1,
		Columns: []ColumnDescriptor{
			{Name: "timestamp", ID: 1, Type: colTypeTimestamp},
			{Name: "rangeID", ID: 2, Type: colTypeInt},
			{Name: "storeID", ID: 3, Type: colTypeInt},
			{Name: "eventType", ID: 4, Type: colTypeString},
			{Name: "otherRangeID", ID: 5, Type: colTypeInt, Nullable: true},
			{Name: "info", ID: 6, Type: colTypeString, Nullable: true},
			{Name: "uniqueID", ID: 7, Type: colTypeInt, DefaultExpr: &uniqueRowIDString},
		},
		NextColumnID: 8,
		Families: []ColumnFamilyDescriptor{
			{Name: "primary", ID: 0, ColumnNames: []string{"timestamp", "uniqueID"}, ColumnIDs: []ColumnID{1, 7}},
			{Name: "fam_2_rangeID", ID: 2, ColumnNames: []string{"rangeID"}, ColumnIDs: []ColumnID{2}, DefaultColumnID: 2},
			{Name: "fam_3_storeID", ID: 3, ColumnNames: []string{"storeID"}, ColumnIDs: []ColumnID{3}, DefaultColumnID: 3},
			{Name: "fam_4_eventType", ID: 4, ColumnNames: []string{"eventType"}, ColumnIDs: []ColumnID{4}, DefaultColumnID: 4},
			{Name: "fam_5_otherRangeID", ID: 5, ColumnNames: []string{"otherRangeID"}, ColumnIDs: []ColumnID{5}, DefaultColumnID: 5},
			{Name: "fam_6_info", ID: 6, ColumnNames: []string{"info"}, ColumnIDs: []ColumnID{6}, DefaultColumnID: 6},
		},
		PrimaryIndex: IndexDescriptor{
			Name:             "primary",
			ID:               1,
			Unique:           true,
			ColumnNames:      []string{"timestamp", "uniqueID"},
			ColumnDirections: []IndexDescriptor_Direction{IndexDescriptor_ASC, IndexDescriptor_ASC},
			ColumnIDs:        []ColumnID{1, 7},
		},
		NextFamilyID:   7,
		NextIndexID:    2,
		Privileges:     NewPrivilegeDescriptor(security.RootUser, SystemDesiredPrivileges(keys.RangeEventTableID)),
		FormatVersion:  InterleavedFormatVersion,
		NextMutationID: 1,
	}

	// UITable is the descriptor for the ui table.
	UITable = TableDescriptor{
		Name:     "ui",
		ID:       keys.UITableID,
		ParentID: 1,
		Version:  1,
		Columns: []ColumnDescriptor{
			{Name: "key", ID: 1, Type: colTypeString},
			{Name: "value", ID: 2, Type: colTypeBytes, Nullable: true},
			{Name: "lastUpdated", ID: 3, Type: ColumnType{SemanticType: ColumnType_TIMESTAMP}},
		},
		NextColumnID: 4,
		Families: []ColumnFamilyDescriptor{
			{Name: "primary", ID: 0, ColumnNames: []string{"key"}, ColumnIDs: singleID1},
			{Name: "fam_2_value", ID: 2, ColumnNames: []string{"value"}, ColumnIDs: []ColumnID{2}, DefaultColumnID: 2},
			{Name: "fam_3_lastUpdated", ID: 3, ColumnNames: []string{"lastUpdated"}, ColumnIDs: []ColumnID{3}, DefaultColumnID: 3},
		},
		NextFamilyID:   4,
		PrimaryIndex:   pk("key"),
		NextIndexID:    2,
		Privileges:     NewPrivilegeDescriptor(security.RootUser, SystemDesiredPrivileges(keys.UITableID)),
		FormatVersion:  InterleavedFormatVersion,
		NextMutationID: 1,
	}

	nowString = "now()"

	// JobsTable is the descriptor for the jobs table.
	JobsTable = TableDescriptor{
		Name:     "jobs",
		ID:       keys.JobsTableID,
		ParentID: 1,
		Version:  1,
		Columns: []ColumnDescriptor{
			{Name: "id", ID: 1, Type: colTypeInt, DefaultExpr: &uniqueRowIDString},
			{Name: "status", ID: 2, Type: colTypeString},
			{Name: "created", ID: 3, Type: colTypeTimestamp, DefaultExpr: &nowString},
			{Name: "payload", ID: 4, Type: colTypeBytes},
		},
		NextColumnID: 5,
		Families: []ColumnFamilyDescriptor{
			{
				Name:        "fam_0_id_status_created_payload",
				ID:          0,
				ColumnNames: []string{"id", "status", "created", "payload"},
				ColumnIDs:   []ColumnID{1, 2, 3, 4},
			},
		},
		NextFamilyID: 1,
		PrimaryIndex: pk("id"),
		Indexes: []IndexDescriptor{
			{
				Name:             "jobs_status_created_idx",
				ID:               2,
				Unique:           false,
				ColumnNames:      []string{"status", "created"},
				ColumnDirections: []IndexDescriptor_Direction{IndexDescriptor_ASC, IndexDescriptor_ASC},
				ColumnIDs:        []ColumnID{2, 3},
				ExtraColumnIDs:   []ColumnID{1},
			},
		},
		NextIndexID:    3,
		Privileges:     NewPrivilegeDescriptor(security.RootUser, SystemDesiredPrivileges(keys.JobsTableID)),
		FormatVersion:  InterleavedFormatVersion,
		NextMutationID: 1,
	}
)

// Create the key/value pair for the default zone config entry.
func createDefaultZoneConfig() roachpb.KeyValue {
	zoneConfig := config.DefaultZoneConfig()
	value := roachpb.Value{}
	if err := value.SetProto(&zoneConfig); err != nil {
		panic(fmt.Sprintf("could not marshal DefaultZoneConfig: %s", err))
	}
	return roachpb.KeyValue{
		Key:   MakeZoneKey(keys.RootNamespaceID),
		Value: value,
	}
}

// addSystemDatabaseToSchema populates the supplied MetadataSchema with the
// System database and its tables. The descriptors for these objects exist
// statically in this file, but a MetadataSchema can be used to persist these
// descriptors to the cockroach store.
func addSystemDatabaseToSchema(target *MetadataSchema) {
	// Add system database.
	target.AddConfigDescriptor(keys.RootNamespaceID, &SystemDB)

	// Add system config tables.
	target.AddConfigDescriptor(keys.SystemDatabaseID, &NamespaceTable)
	target.AddConfigDescriptor(keys.SystemDatabaseID, &DescriptorTable)
	target.AddConfigDescriptor(keys.SystemDatabaseID, &UsersTable)
	target.AddConfigDescriptor(keys.SystemDatabaseID, &ZonesTable)

	// Add all the other system tables.
	target.AddDescriptor(keys.SystemDatabaseID, &LeaseTable)
	target.AddDescriptor(keys.SystemDatabaseID, &EventLogTable)
	target.AddDescriptor(keys.SystemDatabaseID, &RangeEventTable)
	target.AddDescriptor(keys.SystemDatabaseID, &UITable)

	// NOTE(benesch): Installation of the jobs table is intentionally omitted
	// here; it's added via a migration in both fresh clusters and existing
	// clusters. After an upgrade window of a yet-to-be-decided length has passed,
	// we'll remove the migration and add the code to install the jobs table here
	// in the same release. This ensures there's only ever one code path
	// responsible for creating the table. Please follow a similar scheme for any
	// new system tables you create.

	target.otherKV = append(target.otherKV, createDefaultZoneConfig())
}

// IsSystemConfigID returns whether this ID is for a system config object.
func IsSystemConfigID(id ID) bool {
	return id > 0 && id <= keys.MaxSystemConfigDescID
}

// IsReservedID returns whether this ID is for any system object.
func IsReservedID(id ID) bool {
	return id > 0 && id <= keys.MaxReservedDescID
}
