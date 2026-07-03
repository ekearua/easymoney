package service

import "testing"

func TestParseSMSCommand(t *testing.T) {
	command, err := ParseSMSCommand("DATA MTN MTN1GB 08031234567")
	if err != nil {
		t.Fatal(err)
	}
	if command.Kind != "data" || command.NetworkCode != "MTN" || command.PlanCode != "MTN1GB" || command.Phone != "08031234567" {
		t.Fatalf("unexpected data command: %#v", command)
	}
	command, err = ParseSMSCommand("STATUS XG-DATA-8K2Q")
	if err != nil {
		t.Fatal(err)
	}
	if command.Kind != "status" || command.RequestCode != "XG-DATA-8K2Q" {
		t.Fatalf("unexpected status command: %#v", command)
	}
	command, err = ParseSMSCommand("PLANS glo")
	if err != nil {
		t.Fatal(err)
	}
	if command.Kind != "plans" || command.NetworkCode != "GLO" {
		t.Fatalf("unexpected plans command: %#v", command)
	}
	if _, err := ParseSMSCommand("DATA MTN"); err == nil {
		t.Fatal("expected malformed DATA command to fail")
	}
}
