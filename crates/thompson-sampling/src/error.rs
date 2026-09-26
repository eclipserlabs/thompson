//! Error type for the crate.

use std::fmt;

/// Errors produced by posterior construction, arm lookup, and state import.
#[derive(Debug, Clone, PartialEq)]
pub enum Error {
    /// A Beta parameter was not finite and strictly positive.
    InvalidBetaParams {
        /// The offending alpha.
        alpha: f64,
        /// The offending beta.
        beta: f64,
    },
    /// A reward fell outside the `[0, 1]` range the update rules require.
    RewardOutOfRange {
        /// The offending reward.
        reward: f64,
    },
    /// `select` was called with no arms registered.
    NoArms,
    /// An arm id was referenced that has not been registered.
    UnknownArm {
        /// The unknown arm id.
        id: String,
    },
    /// A serialized snapshot could not be decoded.
    Decode(String),
    /// A configuration parameter was not finite (or out of its valid range).
    /// Returned instead of silently degrading selection or learning: NaN
    /// slips through every comparison, so an unchecked NaN threshold,
    /// coefficient, or weight poisons decisions without an error.
    InvalidParameter {
        /// Which parameter was invalid (e.g. "binarize.threshold").
        parameter: String,
        /// The offending value.
        value: f64,
    },
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::InvalidBetaParams { alpha, beta } => write!(
                f,
                "Beta parameters must be finite and > 0, got alpha={alpha}, beta={beta}"
            ),
            Error::RewardOutOfRange { reward } => {
                write!(f, "reward must lie in [0, 1], got {reward}")
            }
            Error::NoArms => write!(f, "no arms registered"),
            Error::UnknownArm { id } => write!(f, "unknown arm: {id}"),
            Error::Decode(msg) => write!(f, "failed to decode snapshot: {msg}"),
            Error::InvalidParameter { parameter, value } => {
                write!(f, "invalid configuration parameter {parameter}: {value}")
            }
        }
    }
}

impl std::error::Error for Error {}

/// Convenience alias.
pub type Result<T> = std::result::Result<T, Error>;
