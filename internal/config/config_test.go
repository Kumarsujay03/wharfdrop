package config

import "testing"

func TestSharesForRespectsPeerList(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c.Update(func(c *Config) {
		c.Shares = []*Share{
			{Name: "all", Path: "/a", Peers: []string{"*"}},
			{Name: "alice", Path: "/b", Peers: []string{"alice"}},
		}
	})
	if got := len(c.SharesFor("alice")); got != 2 {
		t.Fatalf("alice sees %d shares, want 2", got)
	}
	if got := len(c.SharesFor("bob")); got != 1 {
		t.Fatalf("bob sees %d shares, want 1", got)
	}
	if _, ok := c.ShareFor("bob", "alice"); ok {
		t.Fatal("bob must not reach alice's share")
	}
}

func TestDefaultsPersist(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c.Update(func(c *Config) { c.Name = "renamed" })
	again, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Name != "renamed" || again.PeerPort != 48080 {
		t.Fatalf("got name=%q port=%d", again.Name, again.PeerPort)
	}
}
