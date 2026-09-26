-- Which embedding model produced the vectors in vec_chunks.
--
-- The width check at boot catches a model of another width, but two models of
-- the same width embed into different vector spaces: mixing them passes every
-- shape check and silently ruins search. This one row names the model, so boot
-- can refuse a BACKEND_EMBED_MODEL that differs from it (peeq has no automatic
-- re-index).
--
-- Created empty. A database indexed before this table existed gets its row at
-- the next boot (rag.Store.CheckEmbedModel), adopted from the videos whose
-- vectors are stored rather than from config.
CREATE TABLE vec_model (
    id    INTEGER PRIMARY KEY CHECK (id = 1),
    model TEXT NOT NULL
);
