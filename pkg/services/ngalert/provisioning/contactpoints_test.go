package provisioning

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/prometheus/alertmanager/config"
	"github.com/stretchr/testify/require"

	"github.com/grafana/grafana/pkg/components/simplejson"
	"github.com/grafana/grafana/pkg/infra/db"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/services/accesscontrol"
	"github.com/grafana/grafana/pkg/services/accesscontrol/acimpl"
	"github.com/grafana/grafana/pkg/services/accesscontrol/actest"
	"github.com/grafana/grafana/pkg/services/ngalert/api/tooling/definitions"
	"github.com/grafana/grafana/pkg/services/ngalert/models"
	"github.com/grafana/grafana/pkg/services/ngalert/notifier"
	"github.com/grafana/grafana/pkg/services/secrets"
	"github.com/grafana/grafana/pkg/services/secrets/database"
	"github.com/grafana/grafana/pkg/services/secrets/manager"
	"github.com/grafana/grafana/pkg/services/user"
	"github.com/grafana/grafana/pkg/setting"
)

func TestContactPointService(t *testing.T) {
	sqlStore := db.InitTestDB(t)
	secretsService := manager.SetupTestService(t, database.ProvideSecretsStore(sqlStore))
	t.Run("service gets contact points from AM config", func(t *testing.T) {
		sut := createContactPointServiceSut(t, secretsService)

		cps, err := sut.GetContactPoints(context.Background(), cpsQuery(1), nil)
		require.NoError(t, err)

		require.Len(t, cps, 1)
		require.Equal(t, "slack receiver", cps[0].Name)
	})

	t.Run("service filters contact points by name", func(t *testing.T) {
		sut := createContactPointServiceSut(t, secretsService)
		newCp := createTestContactPoint()
		_, err := sut.CreateContactPoint(context.Background(), 1, newCp, models.ProvenanceAPI)
		require.NoError(t, err)

		q := ContactPointQuery{
			OrgID: 1,
			Name:  "slack receiver",
		}
		cps, err := sut.GetContactPoints(context.Background(), q, nil)
		require.NoError(t, err)

		require.Len(t, cps, 1)
		require.Equal(t, "slack receiver", cps[0].Name)
	})

	t.Run("service stitches contact point into org's AM config", func(t *testing.T) {
		sut := createContactPointServiceSut(t, secretsService)
		newCp := createTestContactPoint()

		_, err := sut.CreateContactPoint(context.Background(), 1, newCp, models.ProvenanceAPI)
		require.NoError(t, err)

		cps, err := sut.GetContactPoints(context.Background(), cpsQuery(1), nil)
		require.NoError(t, err)
		require.Len(t, cps, 2)
		require.Equal(t, "test-contact-point", cps[1].Name)
		require.Equal(t, "slack", cps[1].Type)
	})

	t.Run("it's possible to use a custom uid", func(t *testing.T) {
		customUID := "1337"
		sut := createContactPointServiceSut(t, secretsService)
		newCp := createTestContactPoint()
		newCp.UID = customUID

		_, err := sut.CreateContactPoint(context.Background(), 1, newCp, models.ProvenanceAPI)
		require.NoError(t, err)

		cps, err := sut.GetContactPoints(context.Background(), cpsQuery(1), nil)
		require.NoError(t, err)
		require.Len(t, cps, 2)
		require.Equal(t, customUID, cps[1].UID)
	})

	t.Run("it's not possible to use the same uid twice", func(t *testing.T) {
		customUID := "1337"
		sut := createContactPointServiceSut(t, secretsService)
		newCp := createTestContactPoint()
		newCp.UID = customUID

		_, err := sut.CreateContactPoint(context.Background(), 1, newCp, models.ProvenanceAPI)
		require.NoError(t, err)

		_, err = sut.CreateContactPoint(context.Background(), 1, newCp, models.ProvenanceAPI)
		require.Error(t, err)
	})

	t.Run("create rejects contact points that fail validation", func(t *testing.T) {
		sut := createContactPointServiceSut(t, secretsService)
		newCp := createTestContactPoint()
		newCp.Type = ""

		_, err := sut.CreateContactPoint(context.Background(), 1, newCp, models.ProvenanceAPI)

		require.ErrorIs(t, err, ErrValidation)
	})

	t.Run("update rejects contact points with no settings", func(t *testing.T) {
		sut := createContactPointServiceSut(t, secretsService)
		newCp := createTestContactPoint()
		newCp, err := sut.CreateContactPoint(context.Background(), 1, newCp, models.ProvenanceAPI)
		require.NoError(t, err)
		newCp.Settings = nil

		err = sut.UpdateContactPoint(context.Background(), 1, newCp, models.ProvenanceAPI)

		require.ErrorIs(t, err, ErrValidation)
	})

	t.Run("update rejects contact points with no type", func(t *testing.T) {
		sut := createContactPointServiceSut(t, secretsService)
		newCp := createTestContactPoint()
		newCp, err := sut.CreateContactPoint(context.Background(), 1, newCp, models.ProvenanceAPI)
		require.NoError(t, err)
		newCp.Type = ""

		err = sut.UpdateContactPoint(context.Background(), 1, newCp, models.ProvenanceAPI)

		require.ErrorIs(t, err, ErrValidation)
	})

	t.Run("update rejects contact points which fail validation after merging", func(t *testing.T) {
		sut := createContactPointServiceSut(t, secretsService)
		newCp := createTestContactPoint()
		newCp, err := sut.CreateContactPoint(context.Background(), 1, newCp, models.ProvenanceAPI)
		require.NoError(t, err)
		newCp.Settings, _ = simplejson.NewJson([]byte(`{}`))

		err = sut.UpdateContactPoint(context.Background(), 1, newCp, models.ProvenanceAPI)

		require.ErrorIs(t, err, ErrValidation)
	})

	t.Run("default provenance of contact points is none", func(t *testing.T) {
		sut := createContactPointServiceSut(t, secretsService)

		cps, err := sut.GetContactPoints(context.Background(), cpsQuery(1), nil)
		require.NoError(t, err)

		require.Equal(t, models.ProvenanceNone, models.Provenance(cps[0].Provenance))
	})

	t.Run("contact point provenance should be correctly checked", func(t *testing.T) {
		tests := []struct {
			name   string
			from   models.Provenance
			to     models.Provenance
			errNil bool
		}{
			{
				name:   "should be able to update from provenance none to api",
				from:   models.ProvenanceNone,
				to:     models.ProvenanceAPI,
				errNil: true,
			},
			{
				name:   "should be able to update from provenance none to file",
				from:   models.ProvenanceNone,
				to:     models.ProvenanceFile,
				errNil: true,
			},
			{
				name:   "should not be able to update from provenance api to file",
				from:   models.ProvenanceAPI,
				to:     models.ProvenanceFile,
				errNil: false,
			},
			{
				name:   "should not be able to update from provenance api to none",
				from:   models.ProvenanceAPI,
				to:     models.ProvenanceNone,
				errNil: false,
			},
			{
				name:   "should not be able to update from provenance file to api",
				from:   models.ProvenanceFile,
				to:     models.ProvenanceAPI,
				errNil: false,
			},
			{
				name:   "should not be able to update from provenance file to none",
				from:   models.ProvenanceFile,
				to:     models.ProvenanceNone,
				errNil: false,
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				sut := createContactPointServiceSut(t, secretsService)
				newCp := createTestContactPoint()

				newCp, err := sut.CreateContactPoint(context.Background(), 1, newCp, test.from)
				require.NoError(t, err)

				cps, err := sut.GetContactPoints(context.Background(), cpsQuery(1), nil)
				require.NoError(t, err)
				require.Equal(t, newCp.UID, cps[1].UID)
				require.Equal(t, test.from, models.Provenance(cps[1].Provenance))

				err = sut.UpdateContactPoint(context.Background(), 1, newCp, test.to)
				if test.errNil {
					require.NoError(t, err)

					cps, err = sut.GetContactPoints(context.Background(), cpsQuery(1), nil)
					require.NoError(t, err)
					require.Equal(t, newCp.UID, cps[1].UID)
					require.Equal(t, test.to, models.Provenance(cps[1].Provenance))
				} else {
					require.Error(t, err, fmt.Sprintf("cannot change provenance from '%s' to '%s'", test.from, test.to))
				}
			})
		}
	})

	t.Run("service respects concurrency token when updating", func(t *testing.T) {
		sut := createContactPointServiceSut(t, secretsService)
		newCp := createTestContactPoint()
		q := models.GetLatestAlertmanagerConfigurationQuery{
			OrgID: 1,
		}
		config, err := sut.amStore.GetLatestAlertmanagerConfiguration(context.Background(), &q)
		require.NoError(t, err)
		expectedConcurrencyToken := config.ConfigurationHash

		_, err = sut.CreateContactPoint(context.Background(), 1, newCp, models.ProvenanceAPI)
		require.NoError(t, err)

		fake := sut.amStore.(*fakeAMConfigStore)
		intercepted := fake.lastSaveCommand
		require.Equal(t, expectedConcurrencyToken, intercepted.FetchedConfigurationHash)
	})
}

func TestContactPointServiceDecryptRedact(t *testing.T) {
	sqlStore := db.InitTestDB(t)
	secretsService := manager.SetupTestService(t, database.ProvideSecretsStore(sqlStore))
	ac := acimpl.ProvideAccessControl(setting.NewCfg())
	t.Run("GetContactPoints gets redacted contact points by default", func(t *testing.T) {
		sut := createContactPointServiceSut(t, secretsService)

		cps, err := sut.GetContactPoints(context.Background(), cpsQuery(1), nil)
		require.NoError(t, err)

		require.Len(t, cps, 1)
		require.Equal(t, "slack receiver", cps[0].Name)
		require.Equal(t, definitions.RedactedValue, cps[0].Settings.Get("url").MustString())
	})
	t.Run("GetContactPoints errors when Decrypt = true and user does not have permissions", func(t *testing.T) {
		sut := createContactPointServiceSut(t, secretsService)
		sut.ac = ac

		q := cpsQuery(1)
		q.Decrypt = true
		_, err := sut.GetContactPoints(context.Background(), q, &user.SignedInUser{})
		require.ErrorIs(t, err, ErrPermissionDenied)
	})
	t.Run("GetContactPoints errors when Decrypt = true and user is nil", func(t *testing.T) {
		sut := createContactPointServiceSut(t, secretsService)
		sut.ac = ac

		q := cpsQuery(1)
		q.Decrypt = true
		_, err := sut.GetContactPoints(context.Background(), q, nil)
		require.ErrorIs(t, err, ErrPermissionDenied)
	})

	t.Run("GetContactPoints gets decrypted contact points when Decrypt = true and user has permissions", func(t *testing.T) {
		sut := createContactPointServiceSut(t, secretsService)
		sut.ac = ac

		q := cpsQuery(1)
		q.Decrypt = true
		cps, err := sut.GetContactPoints(context.Background(), q, &user.SignedInUser{OrgID: 1, Permissions: map[int64]map[string][]string{
			1: {
				accesscontrol.ActionAlertingProvisioningReadSecrets: nil,
			},
		}})
		require.NoError(t, err)

		require.Len(t, cps, 1)
		require.Equal(t, "slack receiver", cps[0].Name)
		require.Equal(t, "secure url", cps[0].Settings.Get("url").MustString())
	})
}

func TestContactPointInUse(t *testing.T) {
	result := isContactPointInUse("test", []*definitions.Route{
		{
			Receiver: "not-test",
			Routes: []*definitions.Route{
				{
					Receiver: "not-test",
				},
				{
					Receiver: "test",
				},
			},
		},
	})
	require.True(t, result)
	result = isContactPointInUse("test", []*definitions.Route{
		{
			Receiver: "not-test",
			Routes: []*definitions.Route{
				{
					Receiver: "not-test",
				},
				{
					Receiver: "not-test",
				},
			},
		},
	})
	require.False(t, result)
}

func createContactPointServiceSut(t *testing.T, secretService secrets.Service) *ContactPointService {
	// Encrypt secure settings.
	c := &definitions.PostableUserConfig{}
	err := json.Unmarshal([]byte(defaultAlertmanagerConfigJSON), c)
	require.NoError(t, err)
	err = notifier.EncryptReceiverConfigs(c.AlertmanagerConfig.Receivers, func(ctx context.Context, payload []byte) ([]byte, error) {
		return secretService.Encrypt(ctx, payload, secrets.WithoutScope())
	})
	require.NoError(t, err)

	raw, err := json.Marshal(c)
	require.NoError(t, err)

	return &ContactPointService{
		amStore:           newFakeAMConfigStore(string(raw)),
		provenanceStore:   NewFakeProvisioningStore(),
		xact:              newNopTransactionManager(),
		encryptionService: secretService,
		log:               log.NewNopLogger(),
		ac:                actest.FakeAccessControl{},
	}
}

func createTestContactPoint() definitions.EmbeddedContactPoint {
	settings, _ := simplejson.NewJson([]byte(`{"recipient":"value_recipient","token":"value_token"}`))
	return definitions.EmbeddedContactPoint{
		Name:     "test-contact-point",
		Type:     "slack",
		Settings: settings,
	}
}

func cpsQuery(orgID int64) ContactPointQuery {
	return ContactPointQuery{
		OrgID: orgID,
	}
}

func TestStitchReceivers(t *testing.T) {
	type testCase struct {
		name        string
		initial     *definitions.PostableUserConfig
		new         *definitions.PostableGrafanaReceiver
		expModified bool
		expCfg      definitions.PostableApiAlertingConfig
	}

	cases := []testCase{
		{
			name: "non matching receiver by UID, no change",
			new: &definitions.PostableGrafanaReceiver{
				UID: "does not exist",
			},
			expModified: false,
			expCfg:      createTestConfigWithReceivers().AlertmanagerConfig,
		},
		{
			name: "matching receiver with unchanged name, replaces",
			new: &definitions.PostableGrafanaReceiver{
				UID:  "ghi",
				Name: "receiver-2",
				Type: "teams",
			},
			expModified: true,
			expCfg: definitions.PostableApiAlertingConfig{
				Config: definitions.Config{
					Route: &definitions.Route{
						Receiver: "receiver-1",
						Routes: []*definitions.Route{
							{
								Receiver: "receiver-1",
							},
						},
					},
				},
				Receivers: []*definitions.PostableApiReceiver{
					{
						Receiver: config.Receiver{
							Name: "receiver-1",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "abc",
									Name: "receiver-1",
									Type: "slack",
								},
							},
						},
					},
					{
						Receiver: config.Receiver{
							Name: "receiver-2",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "def",
									Name: "receiver-2",
									Type: "slack",
								},
								{
									UID:  "ghi",
									Name: "receiver-2",
									Type: "teams",
								},
								{
									UID:  "jkl",
									Name: "receiver-2",
									Type: "discord",
								},
							},
						},
					},
				},
			},
		},
		{
			name: "rename with only one receiver in group, renames group and references",
			new: &definitions.PostableGrafanaReceiver{
				UID:  "abc",
				Name: "new-receiver",
				Type: "slack",
			},
			expModified: true,
			expCfg: definitions.PostableApiAlertingConfig{
				Config: definitions.Config{
					Route: &definitions.Route{
						Receiver: "new-receiver",
						Routes: []*definitions.Route{
							{
								Receiver: "new-receiver",
							},
						},
					},
				},
				Receivers: []*definitions.PostableApiReceiver{
					{
						Receiver: config.Receiver{
							Name: "new-receiver",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "abc",
									Name: "new-receiver",
									Type: "slack",
								},
							},
						},
					},
					{
						Receiver: config.Receiver{
							Name: "receiver-2",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "def",
									Name: "receiver-2",
									Type: "slack",
								},
								{
									UID:  "ghi",
									Name: "receiver-2",
									Type: "email",
								},
								{
									UID:  "jkl",
									Name: "receiver-2",
									Type: "discord",
								},
							},
						},
					},
				},
			},
		},
		{
			name: "rename to another existing group, moves receiver",
			new: &definitions.PostableGrafanaReceiver{
				UID:  "def",
				Name: "receiver-1",
				Type: "slack",
			},
			expModified: true,
			expCfg: definitions.PostableApiAlertingConfig{
				Config: definitions.Config{
					Route: &definitions.Route{
						Receiver: "receiver-1",
						Routes: []*definitions.Route{
							{
								Receiver: "receiver-1",
							},
						},
					},
				},
				Receivers: []*definitions.PostableApiReceiver{
					{
						Receiver: config.Receiver{
							Name: "receiver-1",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "abc",
									Name: "receiver-1",
									Type: "slack",
								},
								{
									UID:  "def",
									Name: "receiver-1",
									Type: "slack",
								},
							},
						},
					},
					{
						Receiver: config.Receiver{
							Name: "receiver-2",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "ghi",
									Name: "receiver-2",
									Type: "email",
								},
								{
									UID:  "jkl",
									Name: "receiver-2",
									Type: "discord",
								},
							},
						},
					},
				},
			},
		},
		{
			name: "rename to another, larger group",
			initial: &definitions.PostableUserConfig{
				AlertmanagerConfig: definitions.PostableApiAlertingConfig{
					Config: definitions.Config{
						Route: &definitions.Route{
							Receiver: "receiver-1",
							Routes: []*definitions.Route{
								{
									Receiver: "receiver-1",
								},
							},
						},
					},
					Receivers: []*definitions.PostableApiReceiver{
						{
							Receiver: config.Receiver{
								Name: "receiver-1",
							},
							PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
								GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
									{
										UID:  "1",
										Name: "receiver-1",
										Type: "slack",
									},
									{
										UID:  "2",
										Name: "receiver-1",
										Type: "slack",
									},
								},
							},
						},
						{
							Receiver: config.Receiver{
								Name: "receiver-2",
							},
							PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
								GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
									{
										UID:  "3",
										Name: "receiver-2",
										Type: "slack",
									},
									{
										UID:  "4",
										Name: "receiver-2",
										Type: "slack",
									},
									{
										UID:  "5",
										Name: "receiver-2",
										Type: "slack",
									},
								},
							},
						},
					},
				},
			},
			new: &definitions.PostableGrafanaReceiver{
				UID:  "2",
				Name: "receiver-2",
				Type: "slack",
			},
			expModified: true,
			expCfg: definitions.PostableApiAlertingConfig{
				Config: definitions.Config{
					Route: &definitions.Route{
						Receiver: "receiver-1",
						Routes: []*definitions.Route{
							{
								Receiver: "receiver-1",
							},
						},
					},
				},
				Receivers: []*definitions.PostableApiReceiver{
					{
						Receiver: config.Receiver{
							Name: "receiver-1",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "1",
									Name: "receiver-1",
									Type: "slack",
								},
							},
						},
					},
					{
						Receiver: config.Receiver{
							Name: "receiver-2",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "3",
									Name: "receiver-2",
									Type: "slack",
								},
								{
									UID:  "4",
									Name: "receiver-2",
									Type: "slack",
								},
								{
									UID:  "5",
									Name: "receiver-2",
									Type: "slack",
								},
								{
									UID:  "2",
									Name: "receiver-2",
									Type: "slack",
								},
							},
						},
					},
				},
			},
		},
		{
			name: "rename when there are many groups",
			initial: &definitions.PostableUserConfig{
				AlertmanagerConfig: definitions.PostableApiAlertingConfig{
					Config: definitions.Config{
						Route: &definitions.Route{
							Receiver: "receiver-1",
							Routes: []*definitions.Route{
								{
									Receiver: "receiver-1",
								},
							},
						},
					},
					Receivers: []*definitions.PostableApiReceiver{
						{
							Receiver: config.Receiver{
								Name: "receiver-1",
							},
							PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
								GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
									{
										UID:  "1",
										Name: "receiver-1",
										Type: "slack",
									},
									{
										UID:  "2",
										Name: "receiver-1",
										Type: "slack",
									},
								},
							},
						},
						{
							Receiver: config.Receiver{
								Name: "receiver-2",
							},
							PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
								GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
									{
										UID:  "3",
										Name: "receiver-2",
										Type: "slack",
									},
								},
							},
						},
						{
							Receiver: config.Receiver{
								Name: "receiver-3",
							},
							PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
								GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
									{
										UID:  "4",
										Name: "receiver-4",
										Type: "slack",
									},
								},
							},
						},
						{
							Receiver: config.Receiver{
								Name: "receiver-4",
							},
							PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
								GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
									{
										UID:  "5",
										Name: "receiver-4",
										Type: "slack",
									},
								},
							},
						},
					},
				},
			},
			new: &definitions.PostableGrafanaReceiver{
				UID:  "2",
				Name: "receiver-4",
				Type: "slack",
			},
			expModified: true,
			expCfg: definitions.PostableApiAlertingConfig{
				Config: definitions.Config{
					Route: &definitions.Route{
						Receiver: "receiver-1",
						Routes: []*definitions.Route{
							{
								Receiver: "receiver-1",
							},
						},
					},
				},
				Receivers: []*definitions.PostableApiReceiver{
					{
						Receiver: config.Receiver{
							Name: "receiver-1",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "1",
									Name: "receiver-1",
									Type: "slack",
								},
							},
						},
					},
					{
						Receiver: config.Receiver{
							Name: "receiver-2",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "3",
									Name: "receiver-2",
									Type: "slack",
								},
							},
						},
					},
					{
						Receiver: config.Receiver{
							Name: "receiver-3",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "4",
									Name: "receiver-4",
									Type: "slack",
								},
							},
						},
					},
					{
						Receiver: config.Receiver{
							Name: "receiver-4",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "5",
									Name: "receiver-4",
									Type: "slack",
								},
								{
									UID:  "2",
									Name: "receiver-4",
									Type: "slack",
								},
							},
						},
					},
				},
			},
		},
		{
			name: "rename to a name that doesn't exist, creates new group and moves",
			new: &definitions.PostableGrafanaReceiver{
				UID:  "jkl",
				Name: "brand-new-group",
				Type: "opsgenie",
			},
			expModified: true,
			expCfg: definitions.PostableApiAlertingConfig{
				Config: definitions.Config{
					Route: &definitions.Route{
						Receiver: "receiver-1",
						Routes: []*definitions.Route{
							{
								Receiver: "receiver-1",
							},
						},
					},
				},
				Receivers: []*definitions.PostableApiReceiver{
					{
						Receiver: config.Receiver{
							Name: "receiver-1",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "abc",
									Name: "receiver-1",
									Type: "slack",
								},
							},
						},
					},
					{
						Receiver: config.Receiver{
							Name: "receiver-2",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "def",
									Name: "receiver-2",
									Type: "slack",
								},
								{
									UID:  "ghi",
									Name: "receiver-2",
									Type: "email",
								},
							},
						},
					},
					{
						Receiver: config.Receiver{
							Name: "brand-new-group",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "jkl",
									Name: "brand-new-group",
									Type: "opsgenie",
								},
							},
						},
					},
				},
			},
		},
		{
			name:    "rename an inconsistent group in the database, algorithm fixes it",
			initial: createInconsistentTestConfigWithReceivers(),
			new: &definitions.PostableGrafanaReceiver{
				UID:  "ghi",
				Name: "brand-new-group",
				Type: "opsgenie",
			},
			expModified: true,
			expCfg: definitions.PostableApiAlertingConfig{
				Config: definitions.Config{
					Route: &definitions.Route{
						Receiver: "receiver-1",
						Routes: []*definitions.Route{
							{
								Receiver: "receiver-1",
							},
						},
					},
				},
				Receivers: []*definitions.PostableApiReceiver{
					{
						Receiver: config.Receiver{
							Name: "receiver-1",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "abc",
									Name: "receiver-1",
									Type: "slack",
								},
							},
						},
					},
					{
						Receiver: config.Receiver{
							Name: "receiver-2",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "def",
									Name: "receiver-2",
									Type: "slack",
								},
								{
									UID:  "jkl",
									Name: "receiver-2",
									Type: "discord",
								},
							},
						},
					},
					{
						Receiver: config.Receiver{
							Name: "brand-new-group",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "ghi",
									Name: "brand-new-group",
									Type: "opsgenie",
								},
							},
						},
					},
				},
			},
		},
		{
			name: "single item group rename to existing group",
			initial: &definitions.PostableUserConfig{
				AlertmanagerConfig: definitions.PostableApiAlertingConfig{
					Config: definitions.Config{
						Route: &definitions.Route{
							Receiver: "receiver-1",
							Routes: []*definitions.Route{
								{
									Receiver: "receiver-1",
								},
								{
									Receiver: "receiver-2",
								},
							},
						},
					},
					Receivers: []*definitions.PostableApiReceiver{
						{
							Receiver: config.Receiver{
								Name: "receiver-1",
							},
							PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
								GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
									{
										UID:  "1",
										Name: "receiver-1",
										Type: "slack",
									},
									{
										UID:  "2",
										Name: "receiver-1",
										Type: "slack",
									},
								},
							},
						},
						{
							Receiver: config.Receiver{
								Name: "receiver-2",
							},
							PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
								GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
									{
										UID:  "3",
										Name: "receiver-2",
										Type: "slack",
									},
								},
							},
						},
					},
				},
			},
			new: &definitions.PostableGrafanaReceiver{
				UID:  "3",
				Name: "receiver-1",
				Type: "slack",
			},
			expModified: true,
			expCfg: definitions.PostableApiAlertingConfig{
				Config: definitions.Config{
					Route: &definitions.Route{
						Receiver: "receiver-1",
						Routes: []*definitions.Route{
							{
								Receiver: "receiver-1",
							},
							{
								Receiver: "receiver-1",
							},
						},
					},
				},
				Receivers: []*definitions.PostableApiReceiver{
					{
						Receiver: config.Receiver{
							Name: "receiver-1",
						},
						PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
							GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
								{
									UID:  "1",
									Name: "receiver-1",
									Type: "slack",
								},
								{
									UID:  "2",
									Name: "receiver-1",
									Type: "slack",
								},
								{
									UID:  "3",
									Name: "receiver-1",
									Type: "slack",
								},
							},
						},
					},
				},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := createTestConfigWithReceivers()
			if c.initial != nil {
				cfg = c.initial
			}

			modified := stitchReceiver(cfg, c.new)

			require.Equal(t, c.expModified, modified)
			require.Equal(t, c.expCfg, cfg.AlertmanagerConfig)
		})
	}
}

func createTestConfigWithReceivers() *definitions.PostableUserConfig {
	return &definitions.PostableUserConfig{
		AlertmanagerConfig: definitions.PostableApiAlertingConfig{
			Config: definitions.Config{
				Route: &definitions.Route{
					Receiver: "receiver-1",
					Routes: []*definitions.Route{
						{
							Receiver: "receiver-1",
						},
					},
				},
			},
			Receivers: []*definitions.PostableApiReceiver{
				{
					Receiver: config.Receiver{
						Name: "receiver-1",
					},
					PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
						GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
							{
								UID:  "abc",
								Name: "receiver-1",
								Type: "slack",
							},
						},
					},
				},
				{
					Receiver: config.Receiver{
						Name: "receiver-2",
					},
					PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
						GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
							{
								UID:  "def",
								Name: "receiver-2",
								Type: "slack",
							},
							{
								UID:  "ghi",
								Name: "receiver-2",
								Type: "email",
							},
							{
								UID:  "jkl",
								Name: "receiver-2",
								Type: "discord",
							},
						},
					},
				},
			},
		},
	}
}

// This is an invalid config, with inconsistently named receivers (intentionally).
func createInconsistentTestConfigWithReceivers() *definitions.PostableUserConfig {
	return &definitions.PostableUserConfig{
		AlertmanagerConfig: definitions.PostableApiAlertingConfig{
			Config: definitions.Config{
				Route: &definitions.Route{
					Receiver: "receiver-1",
					Routes: []*definitions.Route{
						{
							Receiver: "receiver-1",
						},
					},
				},
			},
			Receivers: []*definitions.PostableApiReceiver{
				{
					Receiver: config.Receiver{
						Name: "receiver-1",
					},
					PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
						GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
							{
								UID:  "abc",
								Name: "receiver-1",
								Type: "slack",
							},
						},
					},
				},
				{
					Receiver: config.Receiver{
						Name: "receiver-2",
					},
					PostableGrafanaReceivers: definitions.PostableGrafanaReceivers{
						GrafanaManagedReceivers: []*definitions.PostableGrafanaReceiver{
							{
								UID:  "def",
								Name: "receiver-2",
								Type: "slack",
							},
							{
								UID:  "ghi",
								Name: "receiver-3",
								Type: "email",
							},
							{
								UID:  "jkl",
								Name: "receiver-2",
								Type: "discord",
							},
						},
					},
				},
			},
		},
	}
}
