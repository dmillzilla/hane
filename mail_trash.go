package main

import (
	"github.com/emersion/go-imap"
	uidplus "github.com/emersion/go-imap-uidplus"
	"github.com/emersion/go-imap/client"
)

func trashProblem(s string) error { return &connectionProblem{"Move to Trash", s} }

// Native MOVE is retriable by source UID. Without MOVE, each COPY result must
// reach durable storage before a later sync may remove the originals. Never
// call Client.UidMove without checking MOVE: its fallback uses global EXPUNGE.
func moveToTrash(im *client.Client, folders []folderInfo, op trashChange) (updated trashChange, done, stale bool, err error) {
	updated = op
	if op.State == "uncertain" || op.State == "copying" {
		updated.State = "uncertain"
		return
	}
	if len(op.Refs) == 0 {
		done = true
		return
	}
	source := op.Refs[0]
	if source.Folder == op.Destination || op.Destination == "" {
		err = trashProblem("Invalid Trash destination; originals kept.")
		return
	}
	destinationFound := false
	sourceFound := false
	for _, f := range folders {
		if f.Name == op.Destination && f.Role == imap.TrashAttr {
			destinationFound = true
		}
		if f.Name == source.Folder && f.Validity == source.Validity {
			sourceFound = true
		}
	}
	if !sourceFound {
		done, stale = true, true
		return
	}
	if !destinationFound {
		err = trashProblem("Trash folder is unavailable; originals kept.")
		return
	}
	var uids []uint32
	for _, r := range op.Refs {
		if r.Folder != source.Folder || r.Validity != source.Validity || r.UID == 0 {
			err = trashProblem("Invalid message identity; originals kept.")
			return
		}
		uids = append(uids, r.UID)
	}
	box, e := im.Select(source.Folder, false)
	if e != nil {
		err = e
		return
	}
	if box.UidValidity != source.Validity {
		done, stale = true, true
		return
	}
	criteria := imap.NewSearchCriteria()
	criteria.Uid = uidSet(uids)
	present, e := im.UidSearch(criteria)
	if e != nil {
		err = e
		return
	}
	if len(present) == 0 {
		done = true
		return
	}
	move, e := im.Support("MOVE")
	if e != nil {
		err = e
		return
	}
	if move && op.State == "" {
		err = im.UidMove(uidSet(present), op.Destination)
		done = err == nil
		return
	}
	plus := uidplus.NewClient(im)
	supported, e := plus.SupportUidPlus()
	if e != nil {
		err = e
		return
	}
	if !supported {
		err = trashProblem("Server needs MOVE or UIDPLUS support. Originals kept.")
		return
	}
	switch op.State {
	case "":
		updated.State = "ready" // Saved by the UI before a COPY is allowed.
	case "ready":
		updated.State = "uncertain" // Any lost reply leaves the source untouched.
		validity, src, dst, e := plus.UidCopy(uidSet(present), op.Destination)
		if e != nil {
			err = e
			return
		}
		copies, e := copyReceipt(op.Destination, validity, src, dst, present)
		if e != nil {
			err = e
			return
		}
		updated.Copies = copies
		updated.State = "copied"
	case "copied":
		// If another client emptied Trash since COPY, keep the originals.
		if len(op.Copies) == 0 {
			err = trashProblem("Missing Trash copy receipt; originals kept.")
			return
		}
		dest, e := im.Select(op.Destination, true)
		if e != nil {
			err = e
			return
		}
		var ids []uint32
		for _, r := range op.Copies {
			if r.Folder != op.Destination || r.Validity != dest.UidValidity || r.UID == 0 {
				err = trashProblem("Trash changed after copying; originals kept.")
				return
			}
			ids = append(ids, r.UID)
		}
		check := imap.NewSearchCriteria()
		check.Uid = uidSet(ids)
		check.WithoutFlags = []string{imap.DeletedFlag}
		found, e := im.UidSearch(check)
		if e != nil {
			err = e
			return
		}
		if len(found) != len(ids) {
			err = trashProblem("A Trash copy is missing; originals kept.")
			return
		}
		box, e = im.Select(source.Folder, false)
		if e != nil {
			err = e
			return
		}
		if box.UidValidity != source.Validity {
			done, stale = true, true
			return
		}
		set := uidSet(present)
		if e = im.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.DeletedFlag}, nil); e != nil {
			err = e
			return
		}
		err = plus.UidExpunge(set, nil)
		done = err == nil
	default:
		err = trashProblem("Move state needs review; originals kept.")
	}
	return
}
func copyReceipt(folder string, validity uint32, src, dst *imap.SeqSet, wanted []uint32) ([]mailRef, error) {
	failure := trashProblem("Trash copy could not be verified. Originals kept; check Trash before trying again.")
	if validity == 0 || src == nil || dst == nil || src.Dynamic() || dst.Dynamic() {
		return nil, failure
	}
	expand := func(set *imap.SeqSet) ([]uint32, bool) {
		var out []uint32
		for _, s := range set.Set {
			if s.Stop < s.Start || uint64(s.Stop)-uint64(s.Start)+1 > uint64(len(wanted)-len(out)) {
				return nil, false
			}
			for n := s.Start; ; n++ {
				out = append(out, n)
				if n == s.Stop {
					break
				}
			}
		}
		return out, len(out) == len(wanted)
	}
	sources, ok := expand(src)
	if !ok {
		return nil, failure
	}
	destinations, ok := expand(dst)
	if !ok {
		return nil, failure
	}
	expected := map[uint32]bool{}
	for _, id := range wanted {
		expected[id] = true
	}
	for _, id := range sources {
		if !expected[id] {
			return nil, failure
		}
		delete(expected, id)
	}
	if len(expected) != 0 {
		return nil, failure
	}
	var out []mailRef
	for _, id := range destinations {
		out = append(out, mailRef{folder, validity, id})
	}
	return out, nil
}
func removeMovedCache(a *accountMail, refs []mailRef) {
	moved := map[mailRef]bool{}
	for _, r := range refs {
		moved[r] = true
	}
	kept := a.Messages[:0]
	for _, r := range a.Messages {
		if !moved[r.Ref] {
			kept = append(kept, r)
			continue
		}
		for i := range a.Folders {
			f := &a.Folders[i]
			if f.Name == r.Ref.Folder {
				if f.Total > 0 {
					f.Total--
				}
				if !r.Seen && f.Unread > 0 {
					f.Unread--
				}
				if f.Loaded > 0 {
					f.Loaded--
				}
			}
		}
	}
	a.Messages = kept
}
