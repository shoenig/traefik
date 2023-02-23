package acme

import (
	"errors"
	"sync"
	"testing"

	"github.com/go-acme/lego/v4/acme"
	"github.com/go-acme/lego/v4/registration"
	"github.com/hashicorp/nomad/api"
	"github.com/stretchr/testify/require"
	"github.com/traefik/traefik/v2/pkg/types"
)

// assert NomadStore implements the Store interface
var _ Store = (*NomadStore)(nil)

// set the test case environment to match that of Traefik running as a Nomad Task
func setNomadStoreEnv(t *testing.T) {
	t.Setenv(envNomadToken, "659c8d84-1b2d-73ee-f6bb-6b2421055a61")
	t.Setenv(envNomadSecretsDir, "/some/path")
	t.Setenv(envNomadJob, "job1")
	t.Setenv(envNomadGroup, "group1")
	t.Setenv(envNomadTask, "task1")
}

func TestNomadStore_MaybeNewNomadStore(t *testing.T) {
	cases := []struct {
		name        string
		envToken    string
		secretsDir  string
		expCreation bool
	}{
		{
			name:        "no env set",
			expCreation: false,
		},
		{
			name:        "only token set",
			envToken:    "659c8d84-1b2d-73ee-f6bb-6b2421055a61",
			expCreation: false,
		},
		{
			name:        "only secrets set",
			secretsDir:  "/some/path",
			expCreation: false,
		},
		{
			name:        "both set",
			envToken:    "659c8d84-1b2d-73ee-f6bb-6b2421055a61",
			secretsDir:  "/some/path",
			expCreation: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envNomadToken, tc.envToken)
			t.Setenv(envNomadSecretsDir, tc.secretsDir)
			ns := MaybeNewNomadStore()
			require.Equal(t, tc.expCreation, ns != nil)
		})
	}
}

func TestNomadStore_SetResolver(t *testing.T) {
	setNomadStoreEnv(t)

	t.Run("explicit variables path", func(t *testing.T) {
		ns := MaybeNewNomadStore()
		ns.SetResolver("pebble", "nomad://traefik/acme")
		require.Equal(t, "traefik/acme", ns.paths["pebble"])
	})

	t.Run("automatic variables path", func(t *testing.T) {
		ns := MaybeNewNomadStore()
		ns.SetResolver("pebble", "nomad://") // auto generate
		require.Equal(t, "nomad/jobs/job1/group1/task1/acme/pebble", ns.paths["pebble"])
	})
}

func TestNomadStore_GetAccount(t *testing.T) {
	setNomadStoreEnv(t)

	t.Run("missing resolver", func(t *testing.T) {
		ns := MaybeNewNomadStore()
		_, err := ns.GetAccount("le")
		require.ErrorIs(t, err, ErrNoNomadVariableForResolver)
	})

	t.Run("endpoint error", func(t *testing.T) {
		mock := newMockNomadVariablesAPI()
		mock.getVarItemsErr = errors.New("oops")
		ns := MaybeNewNomadStore()
		ns.client = mock
		ns.SetResolver("le", "nomad://")
		_, err := ns.GetAccount("le")
		require.EqualError(t, err, "oops")
	})

	t.Run("account is absent", func(t *testing.T) {
		ns := MaybeNewNomadStore()
		ns.client = newMockNomadVariablesAPI()
		ns.SetResolver("le", "nomad://")
		acct, err := ns.GetAccount("le")
		require.NoError(t, err)
		require.Nil(t, acct)
	})

	t.Run("account is present", func(t *testing.T) {
		mock := newMockNomadVariablesAPI()
		ns := MaybeNewNomadStore()
		ns.client = mock
		_, _, err := ns.client.Create(&api.Variable{
			Path:  "nomad/jobs/job1/group1/task1/acme/le/account",
			Items: api.VariableItems{"account": account1JSON},
		}, nil)
		require.NoError(t, err)
		ns.SetResolver("le", "nomad://")
		acct, err := ns.GetAccount("le")
		require.NoError(t, err)
		require.Equal(t, "test@example.com", acct.Email)
		require.Equal(t, 1, mock.getVarHitCounter)

		// account is in cache now, another lookup should not hit nomad
		acct2, err2 := ns.GetAccount("le")
		require.NoError(t, err2)
		require.Equal(t, "test@example.com", acct2.Email)
		require.Equal(t, 1, mock.getVarHitCounter)
	})
}

func TestNomadStore_SaveAccount(t *testing.T) {
	setNomadStoreEnv(t)

	t.Run("missing resolver", func(t *testing.T) {
		ns := MaybeNewNomadStore()
		err := ns.SaveAccount("le", account1)
		require.ErrorIs(t, err, ErrNoNomadVariableForResolver)
	})

	t.Run("ok", func(t *testing.T) {
		mock := newMockNomadVariablesAPI()
		ns := MaybeNewNomadStore()
		ns.client = mock
		ns.SetResolver("le", "nomad://")
		err := ns.SaveAccount("le", account1)
		require.NoError(t, err)
		require.Equal(t, 1, mock.createHitCounter)
	})
}

func TestNomadStore_GetCertificates(t *testing.T) {
	setNomadStoreEnv(t)

	t.Run("missing resolver", func(t *testing.T) {
		ns := MaybeNewNomadStore()
		_, err := ns.GetCertificates("le")
		require.ErrorIs(t, err, ErrNoNomadVariableForResolver)
	})

	t.Run("endpoint error", func(t *testing.T) {
		mock := newMockNomadVariablesAPI()
		mock.getVarItemsErr = errors.New("oops")
		ns := MaybeNewNomadStore()
		ns.client = mock
		ns.SetResolver("le", "nomad://")
		_, err := ns.GetCertificates("le")
		require.EqualError(t, err, "oops")
	})

	t.Run("certs are absent", func(t *testing.T) {
		ns := MaybeNewNomadStore()
		ns.client = newMockNomadVariablesAPI()
		ns.SetResolver("le", "nomad://")
		certs, err := ns.GetCertificates("le")
		require.NoError(t, err)
		require.Nil(t, certs)
	})

	t.Run("certs are present", func(t *testing.T) {
		mock := newMockNomadVariablesAPI()
		ns := MaybeNewNomadStore()
		ns.client = mock
		_, _, err := ns.client.Create(&api.Variable{
			Path:  "nomad/jobs/job1/group1/task1/acme/le/certificates",
			Items: api.VariableItems{"certificates": certs1JSON},
		}, nil)
		require.NoError(t, err)
		ns.SetResolver("le", "nomad://")
		certs, err := ns.GetCertificates("le")
		require.NoError(t, err)
		require.Equal(t, "default", certs[0].Store)
		require.Equal(t, 1, mock.getVarHitCounter)

		// second lookup should not hit nomad again
		certs2, err2 := ns.GetCertificates("le")
		require.NoError(t, err2)
		require.Equal(t, "default", certs2[0].Store)
		require.Equal(t, 1, mock.getVarHitCounter)
	})
}

func TestNomadStore_SaveCertificates(t *testing.T) {
	setNomadStoreEnv(t)

	t.Run("missing resolver", func(t *testing.T) {
		ns := MaybeNewNomadStore()
		err := ns.SaveCertificates("le", []*CertAndStore{cert1})
		require.ErrorIs(t, err, ErrNoNomadVariableForResolver)
	})

	t.Run("endpoint error", func(t *testing.T) {
		mock := newMockNomadVariablesAPI()
		mock.createErr = errors.New("oops")
		ns := MaybeNewNomadStore()
		ns.client = mock
		ns.SetResolver("le", "nomad://")
		err := ns.SaveCertificates("le", []*CertAndStore{cert1})
		require.EqualError(t, err, "oops")
	})

	t.Run("ok", func(t *testing.T) {
		mock := newMockNomadVariablesAPI()
		ns := MaybeNewNomadStore()
		ns.client = mock
		ns.SetResolver("le", "nomad://")
		err := ns.SaveCertificates("le", []*CertAndStore{cert1})
		require.NoError(t, err)
		require.Equal(t, 1, mock.createHitCounter)
	})
}

func newMockNomadVariablesAPI() *mockNomadVariablesAPI {
	return &mockNomadVariablesAPI{
		variables: make(map[string]*api.Variable),
	}
}

type mockNomadVariablesAPI struct {
	createErr      error
	getVarItemsErr error

	lock             sync.Mutex
	variables        map[string]*api.Variable
	createHitCounter int
	getVarHitCounter int
}

func (m *mockNomadVariablesAPI) Create(v *api.Variable, qo *api.WriteOptions) (*api.Variable, *api.WriteMeta, error) {
	m.lock.Lock()
	defer m.lock.Unlock()

	m.createHitCounter++

	if m.createErr != nil {
		return nil, nil, m.createErr
	}

	m.variables[v.Path] = v
	return v, nil, nil
}

func (m *mockNomadVariablesAPI) GetVariableItems(path string, qo *api.QueryOptions) (api.VariableItems, *api.QueryMeta, error) {
	m.lock.Lock()
	defer m.lock.Unlock()

	m.getVarHitCounter++

	if m.getVarItemsErr != nil {
		return nil, nil, m.getVarItemsErr
	}

	v, exists := m.variables[path]
	if !exists {
		return nil, nil, api.ErrVariablePathNotFound
	}
	return v.Items, nil, nil
}

const (
	account1JSON = `
{
  "Email": "test@example.com",
  "Registration": {
    "Body": {
      "status": "valid",
      "contact": ["test@example.com"],
      "termsOfServiceAgreed": true
    },
    "URI": "https://example.com/dir"
  },
  "PrivateKey": [1, 2, 3, 4, 5],
  "KeyType": "FAKE"
}
`
)

var (
	account1 = &Account{
		Email: "test@example.com",
		Registration: &registration.Resource{
			Body: acme.Account{
				Status:               "valid",
				Contact:              []string{"test@example.com"},
				TermsOfServiceAgreed: true,
			},
			URI: "https://example.com/dir",
		},
		PrivateKey: []byte{1, 2, 3, 4, 5},
		KeyType:    "FAKE",
	}
)

const (
	certs1JSON = `
[{
  "Domain": {
    "Main": "subject",
    "SANs": ["one", "two"]
  },
  "Certificate": [9, 8, 7, 6, 5],
  "Key": [5, 6, 7, 8, 9],
  "Store": "default"
}]
`
)

var (
	cert1 = &CertAndStore{
		Certificate: Certificate{
			Domain: types.Domain{
				Main: "subject",
				SANs: []string{"one", "two"},
			},
			Certificate: []byte{9, 8, 7, 6, 5},
			Key:         []byte{5, 6, 7, 8, 9},
		},
		Store: "default",
	}
)
