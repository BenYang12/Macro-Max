-- Undo of 000006. Nothing references kroger_tokens, so a plain drop is enough.
--
-- Worth naming the consequence: rolling this back DESTROYS the stored consent,
-- and the only way to get it back is sending the user through Kroger's browser
-- authorization screen again. That's the correct behavior for a down migration
-- (undo means undo), but it isn't the harmless table-drop it looks like.
DROP TABLE kroger_tokens;
