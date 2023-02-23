package acme

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/hashicorp/nomad/api"
	"github.com/shoenig/netlog"
)

var (
	// ErrNoNomadVariableForResolver should never happen, but exists in case we try
	// to reference a resolver for which no Nomad Variable was configured to be
	// the backing store of.
	ErrNoNomadVariableForResolver = errors.New("no nomad variable set for resolver")
)

const (
	envNomadToken      = "NOMAD_TOKEN"
	envNomadSecretsDir = "NOMAD_SECRETS_DIR"
	envNomadJob        = "NOMAD_JOB_NAME"
	envNomadGroup      = "NOMAD_GROUP"
	envNomadTask       = "NOMAD_TASK_NAME"
)

const (
	nomadStoreAccountType = "account"
	nomadStoreCertsType   = "certificates"
)

// nomadVariablesAPI is the subset of the Nomad API Client needed for managing
// ACME certificates in the NomadStore.
type nomadVariablesAPI interface {
	Create(v *api.Variable, qo *api.WriteOptions) (*api.Variable, *api.WriteMeta, error)
	GetVariableItems(path string, qo *api.QueryOptions) (api.VariableItems, *api.QueryMeta, error)
}

// NomadStore is an implementation of Store where certificates are
// conveniently persisted by Nomad as Nomad Variables.
type NomadStore struct {
	client nomadVariablesAPI

	lock         sync.Mutex
	certCache    map[string][]*CertAndStore // resolver name to certs
	accountCache map[string]*Account        // resolver name to account
	paths        map[string]string          // resolver name to variables path
}

// MaybeNewNomadStore conditionally creates a NomadStore if Traefik is being run
// as a Nomad 1.5+ task. Returns nil if Traefik is not being run as a Nomad task.
func MaybeNewNomadStore() *NomadStore {
	if os.Getenv(envNomadToken) == "" || os.Getenv(envNomadSecretsDir) == "" {
		// these environment variables will be set if we are a compatible Nomad task
		return nil
	}
	return &NomadStore{
		paths:        make(map[string]string),
		certCache:    make(map[string][]*CertAndStore),
		accountCache: make(map[string]*Account),
		client:       api.TaskClient(nil).Variables(),
	}
}

// SetResolver associates a resolver to a Nomad Variables path, where certificate
// and account information will be persistently stored. SetResolver may be called
// for any number of resolvers, but each one should be given its own path.
//
// If variablesPath is not specified (i.e. "nomad://"), then Traefik will automatically
// use the Job, Group, and Task of the Traefik Nomad Task to generate a sensible
// variables path: "nomad://nomad/jobs/<job>/<group>/<task>/acme/<resolver>".
func (ns *NomadStore) SetResolver(resolverName, variablesPath string) *NomadStore {
	p := strings.TrimPrefix(variablesPath, "nomad://")
	if p == "" {
		job := os.Getenv(envNomadJob)
		group := os.Getenv(envNomadGroup)
		task := os.Getenv(envNomadTask)
		resolver := strings.ToLower(resolverName)
		p = fmt.Sprintf("nomad/jobs/%s/%s/%s/acme/%s", job, group, task, resolver)
	}

	ns.lock.Lock()
	defer ns.lock.Unlock()

	ns.paths[resolverName] = p
	return ns
}

// getPath returns the Nomad Variables path for the specified resolver and item type
//
// caller must hold ns.lock
func (ns *NomadStore) pathForResolverLocked(resolverName, itemType string) (string, bool) {
	resolverPath, exists := ns.paths[resolverName]
	if !exists || resolverPath == "" {
		return "", false
	}
	return path.Join(resolverPath, itemType), true
}

func (ns *NomadStore) GetAccount(resolverName string) (*Account, error) {
	ns.lock.Lock()
	defer ns.lock.Unlock()

	// optimistically check the account cache for this resolver
	if account, exists := ns.accountCache[resolverName]; exists {
		return account, nil
	}

	// determine nomad variable path
	accountPath, exists := ns.pathForResolverLocked(resolverName, nomadStoreAccountType)
	if !exists {
		return nil, ErrNoNomadVariableForResolver
	}

	netlog.Yellow("NomadStore.GetAccount", "resolverName", resolverName, "accountPath", accountPath, "exists", exists)

	// lookup the account from nomad variables
	account, err := get[*Account](ns, accountPath, nomadStoreAccountType)
	if errors.Is(err, api.ErrVariablePathNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	// add the account to the account cache
	ns.accountCache[resolverName] = account

	return account, nil
}

func (ns *NomadStore) SaveAccount(resolverName string, account *Account) error {
	ns.lock.Lock()
	defer ns.lock.Unlock()

	// set account in the write through cache
	ns.accountCache[resolverName] = account

	netlog.Yellow("NomadStore.SaveAccount", "resolverName", resolverName, "account.Email", account.Email)

	// determine nomad variable path for account
	accountPath, exists := ns.pathForResolverLocked(resolverName, nomadStoreAccountType)
	if !exists {
		return ErrNoNomadVariableForResolver
	}

	// save the account in nomad variable
	return put(ns, accountPath, nomadStoreAccountType, account)
}

func (ns *NomadStore) GetCertificates(resolverName string) ([]*CertAndStore, error) {
	ns.lock.Lock()
	defer ns.lock.Unlock()

	// optimistically check the certs cache for this resolver
	if certificates, exists := ns.certCache[resolverName]; exists {
		return certificates, nil
	}

	// determine nomad variable path
	certPath, exists := ns.pathForResolverLocked(resolverName, nomadStoreCertsType)
	if !exists {
		return nil, ErrNoNomadVariableForResolver
	}

	netlog.Yellow("NomadStore.GetCertificates", "resolverName", resolverName, "certPath", certPath)

	// lookup the certificates from nomad variables
	certificates, err := get[[]*CertAndStore](ns, certPath, nomadStoreCertsType)
	if errors.Is(err, api.ErrVariablePathNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	// add the certs to the cache for this resolver
	// todo: should we drop empty certs like localstore?
	ns.certCache[resolverName] = certificates

	return certificates, nil
}

func (ns *NomadStore) SaveCertificates(resolverName string, certificates []*CertAndStore) error {
	ns.lock.Lock()
	defer ns.lock.Unlock()

	// set certificates in the write through cache
	ns.certCache[resolverName] = certificates

	netlog.Yellow("NomadStore.SaveCertificates", "resolverName", resolverName)

	// determine nomad variables path for certificates
	certPath, exists := ns.pathForResolverLocked(resolverName, nomadStoreCertsType)
	if !exists {
		return ErrNoNomadVariableForResolver
	}

	// save the certificates in nomad veriable
	return put(ns, certPath, nomadStoreCertsType, certificates)
}

func put[T any](ns *NomadStore, varPath, key string, item T) error {
	netlog.Purple("put()", "varPath", varPath, "key", key)

	b, err := json.Marshal(item)
	if err != nil {
		return err
	}
	variable := &api.Variable{
		Path:  varPath,
		Items: map[string]string{key: string(b)},
	}
	_, _, err = ns.client.Create(variable, nil)
	return err
}

func get[T any](ns *NomadStore, varPath, key string) (T, error) {
	netlog.Purple("get()", "varPath", varPath, "key", key)

	var value T
	items, _, err := ns.client.GetVariableItems(varPath, nil)
	if err != nil {
		return value, err
	}
	s := items[key]
	if err = json.Unmarshal([]byte(s), &value); err != nil {
		return value, err
	}
	return value, nil
}
