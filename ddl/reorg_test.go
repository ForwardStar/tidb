// Copyright 2015 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ddl_test

import (
	"context"
	"testing"

	"github.com/ngaut/pools"
	"github.com/pingcap/tidb/ddl"
	"github.com/pingcap/tidb/kv"
	"github.com/pingcap/tidb/meta"
	"github.com/pingcap/tidb/parser/model"
	"github.com/pingcap/tidb/sessiontxn"
	"github.com/pingcap/tidb/testkit"
	"github.com/pingcap/tidb/types"
	"github.com/stretchr/testify/require"
)

func TestReorgOwner(t *testing.T) {
	store, domain := testkit.CreateMockStoreAndDomainWithSchemaLease(t, testLease)

	d1 := domain.DDL()

	sctx := testkit.NewTestKit(t, store).Session()

	require.True(t, d1.OwnerManager().IsOwner())

	domain.InfoCache()
	d2 := ddl.NewDDL(
		context.Background(),
		ddl.WithEtcdClient(domain.EtcdClient()),
		ddl.WithInfoCache(domain.InfoCache()),
		ddl.WithStore(store),
		ddl.WithLease(testLease),
	)

	err := d2.Start(pools.NewResourcePool(func() (pools.Resource, error) {
		session := testkit.NewTestKit(t, store).Session()
		session.GetSessionVars().CommonGlobalLoaded = true
		return session, nil
	}, 20, 20, 5))
	require.NoError(t, err)

	defer func() {
		err := d2.Stop()
		require.NoError(t, err)
	}()

	dbInfo, err := testSchemaInfo(store, "test_reorg")
	require.NoError(t, err)
	testCreateSchema(t, sctx, d1, dbInfo)

	tblInfo, err := testTableInfo(store, "t", 3)
	require.NoError(t, err)
	testCreateTable(t, sctx, d1, dbInfo, tblInfo)
	tbl, err := testGetTableWithError(store, dbInfo.ID, tblInfo.ID)
	require.NoError(t, err)

	num := 10
	sctx = testkit.NewTestKit(t, store).Session()
	err = sessiontxn.NewTxn(context.Background(), sctx)
	require.NoError(t, err)
	for i := 0; i < num; i++ {
		_, err := tbl.AddRecord(sctx, types.MakeDatums(i, i, i))
		require.NoError(t, err)
	}
	require.NoError(t, sctx.CommitTxn(context.Background()))

	tc := &ddl.TestDDLCallback{}
	tc.OnJobRunBeforeExported = func(job *model.Job) {
		if job.SchemaState == model.StateDeleteReorganization {
			err = d1.Stop()
			require.NoError(t, err)
		}
	}

	d1.SetHook(tc)

	testDropSchema(t, sctx, d1, dbInfo)

	err = kv.RunInNewTxn(kv.WithInternalSourceType(context.Background(), kv.InternalTxnDDL), store, false, func(ctx context.Context, txn kv.Transaction) error {
		m := meta.NewMeta(txn)
		db, err1 := m.GetDatabase(dbInfo.ID)
		require.NoError(t, err1)
		require.Nil(t, db)
		return nil
	})
	require.NoError(t, err)
}
