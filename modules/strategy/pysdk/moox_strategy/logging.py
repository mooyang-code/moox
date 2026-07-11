import logging

logger = logging.getLogger("moox.strategy")
def strategy_log(message: str) -> None:
    logger.info(message)
