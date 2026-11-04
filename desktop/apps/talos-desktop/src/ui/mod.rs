//! Reusable presentation primitives for the Talos desktop shell.
//!
//! The modules here are deliberately stateless: they paint or animate using the
//! caller's data and return responses. Application state lives in [`crate::state`].

pub mod composer;
pub mod icons;
pub mod markdown;
pub mod motion;
pub mod timeline;
pub mod widgets;
