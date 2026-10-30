//go:build linux

package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/godbus/dbus/v5"
	ss "github.com/zalando/go-keyring/secret_service"
)

const secretsService = "org.freedesktop.secrets"
const secretsPath = "/org/freedesktop/secrets"
const secretsInterface = "org.freedesktop.Secret.Service"

// Leave the desktop's default keyring alone. The dedicated collection's unlock
// dialog occurs once per desktop session, rather than per app launch.
func mailCollection(svc *ss.SecretService, create bool) (dbus.BusObject, error) {
	service := svc.Object(secretsService, dbus.ObjectPath(secretsPath))
	var collections []dbus.ObjectPath
	value, err := service.GetProperty(secretsInterface + ".Collections")
	if err != nil {
		return nil, err
	}
	if err = value.Store(&collections); err != nil {
		return nil, err
	}
	path := dbus.ObjectPath("/")
	created := false
	for _, candidate := range collections {
		label, err := svc.Object(secretsService, candidate).GetProperty("org.freedesktop.Secret.Collection.Label")
		if err != nil {
			return nil, err
		}
		if label.Value() == credentialCollectionName {
			if path != "/" {
				return nil, errors.New("multiple " + credentialCollectionName + " keyrings found")
			}
			path = candidate
		}
	}
	if path == "/" {
		if !create {
			return nil, errors.New(credentialCollectionName + " keyring has not been created")
		}
		var err error
		path, err = createMailCollection(svc)
		if err != nil {
			return nil, err
		}
		created = true
	}
	if err := svc.Unlock(path); err != nil {
		return nil, err
	}
	// GNOME permits empty passwords, which produce plaintext files. Inspect
	// only this collection's magic bytes before adding or reading mail secrets.
	var gnome bool
	if err := svc.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.gnome.keyring").Store(&gnome); err != nil {
		return nil, err
	}
	if gnome {
		dir := os.Getenv("XDG_DATA_HOME")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			dir = filepath.Join(home, ".local", "share")
		}
		filename := filepath.Join(dir, "keyrings", filepath.Base(string(path))+".keyring")
		if err := encryptedKeyringFile(filename); err != nil {
			// A rejected, newly created empty collection contains no user data.
			// Remove it so retrying can ask for a non-empty password again.
			if created {
				collection := svc.Object(secretsService, path)
				items, searchErr := svc.SearchItems(collection, map[string]string{})
				if searchErr == nil && len(items) == 0 {
					_ = collection.Call("org.freedesktop.Secret.Collection.Delete", 0).Err
				}
			}
			return nil, err
		}
	}
	return svc.Object(secretsService, path), nil
}

func createMailCollection(svc *ss.SecretService) (dbus.ObjectPath, error) {
	var path, prompt dbus.ObjectPath
	properties := map[string]dbus.Variant{"org.freedesktop.Secret.Collection.Label": dbus.MakeVariant(credentialCollectionName)}
	err := svc.Object(secretsService, dbus.ObjectPath(secretsPath)).Call(secretsInterface+".CreateCollection", 0, properties, "").Store(&path, &prompt)
	if err != nil {
		return "", err
	}
	if prompt != "/" {
		const promptInterface = "org.freedesktop.Secret.Prompt"
		opts := []dbus.MatchOption{dbus.WithMatchObjectPath(prompt), dbus.WithMatchInterface(promptInterface), dbus.WithMatchMember("Completed")}
		if err = svc.AddMatchSignal(opts...); err != nil {
			return "", err
		}
		defer svc.RemoveMatchSignal(opts...)
		signals := make(chan *dbus.Signal, 4)
		svc.Signal(signals)
		defer svc.RemoveSignal(signals)
		if err = svc.Object(secretsService, prompt).Call(promptInterface+".Prompt", 0, "").Err; err != nil {
			return "", err
		}
		for signal := range signals {
			if signal.Path != prompt || signal.Name != promptInterface+".Completed" {
				continue
			}
			if len(signal.Body) != 2 {
				return "", errors.New("invalid keyring prompt response")
			}
			dismissed, ok := signal.Body[0].(bool)
			if !ok || dismissed {
				return "", errors.New("keyring creation cancelled")
			}
			result, ok := signal.Body[1].(dbus.Variant)
			if !ok {
				return "", errors.New("invalid keyring prompt result")
			}
			path, ok = result.Value().(dbus.ObjectPath)
			if !ok {
				return "", errors.New("invalid keyring collection")
			}
			break
		}
	}
	if path == "/" || !path.IsValid() {
		return "", errors.New("keyring creation failed")
	}
	return path, nil
}

func encryptedKeyringFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("cannot verify keyring encryption")
	}
	defer f.Close()
	var header [16]byte
	_, err = io.ReadFull(f, header[:])
	if err != nil || !bytes.HasPrefix(header[:], []byte("GnomeKeyring\n\r\x00\n")) {
		return errors.New("the " + credentialCollectionName + " keyring needs a non-empty password")
	}
	return nil
}

func nativeCredentialGet(key string) (string, error) {
	svc, err := ss.NewSecretService()
	if err != nil {
		return "", err
	}
	collection, err := mailCollection(svc, false)
	if err != nil {
		return "", err
	}
	items, err := svc.SearchItems(collection, map[string]string{"service": credentialService, "username": key})
	if err != nil {
		return "", err
	}
	if len(items) != 1 {
		return "", errors.New("saved credential missing or ambiguous")
	}
	if err = svc.Unlock(items[0]); err != nil {
		return "", err
	}
	session, err := svc.OpenSession()
	if err != nil {
		return "", err
	}
	defer svc.Close(session)
	secret, err := svc.GetSecret(items[0], session.Path())
	if err != nil {
		return "", err
	}
	return string(secret.Value), nil
}

func nativeCredentialSet(key, password string) error {
	svc, err := ss.NewSecretService()
	if err != nil {
		return err
	}
	collection, err := mailCollection(svc, true)
	if err != nil {
		return err
	}
	session, err := svc.OpenSession()
	if err != nil {
		return err
	}
	defer svc.Close(session)
	return svc.CreateItem(collection, "Hane mail app password", map[string]string{"service": credentialService, "username": key}, ss.NewSecret(session.Path(), password))
}
